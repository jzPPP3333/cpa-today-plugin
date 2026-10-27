// Package today 实现 today.ai 的上游协议。
//
// 协议链（国际版，2026-10 实测）：
//
//	浏览器 session cookie (__Secure-better-auth.session_token)
//	  └─ POST https://today.ai/api/token {audience, expiresIn}
//	       → {token, expiresIn}            ← API JWT
//	          └─ Authorization: Bearer <JWT> → https://api.today.ai/v1/*
//
// 对话不是 OpenAI 协议：
//
//	POST /v1/messages {content, messageType:"text", clientMessageId, threadId?}
//	     → 202 {threadId, messageId}
//	GET  /v1/conversations/messages?isMainChat=true&limit=N
//	     → data.items[]，取 threadId 匹配、role=assistant、state=completed 的那条，
//	       正文在 body.parts[] 里 messageType=text 的 content.text。
//
// 上游没有 SSE，只能轮询；适配层把轮询结果伪流式地分片吐给宿主。
package today

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"cpa-today-plugin/internal/core"
	"cpa-today-plugin/internal/hostbridge"
)

const (
	GlobalAppBase  = "https://today.ai"
	GlobalAPIBase  = "https://api.today.ai"
	GlobalAuthBase = "https://auth.today.ai"

	// CN 版（todayai.cn），协议同形，域名不同。
	CNAppBase  = "https://todayai.cn"
	CNAPIBase  = "https://api.todayai.cn"
	CNAuthBase = "https://auth.todayai.cn"
)

// Model 是上游模型选项。
type Model struct {
	OptionID string // model.deepseek-v4.1-flash
	ID       string // deepseek-v4.1-flash
	Title    string
}

// 已知模型选项（/v1/pricing/model-option 的合法取值）。
// KnownModels 是已知模型的显示名表，不是白名单。
//
// 真实可用清单以 /v1/pricing/status 的 modelOptions 为准（随套餐变化），
// 这里只负责给常见模型一个体面的显示名；表里没有的模型仍可正常调用，
// 走 ModelByID 的动态回退分支。
var KnownModels = []Model{
	{OptionID: "model.deepseek-v4.1-flash", ID: "deepseek-v4.1-flash", Title: "DeepSeek V4.1 Flash"},
	{OptionID: "model.glm-5.3", ID: "glm-5.3", Title: "GLM 5.3"},
	{OptionID: "model.minimax-m31-flash", ID: "minimax-m31-flash", Title: "MiniMax M3.1 Flash"},
}

// ModelByID 解析客户端模型名。
//
// 上游的真实模型清单来自 /v1/pricing/status，会随账号套餐变化，
// 不能只认硬编码表：否则探测到的新模型（如 minimax-m31-flash）能出现在
// /v1/models 里，但执行时查表失败直接 404。因此表里查不到时按上游的
// optionId 约定（model.<id>）构造，交给上游判定是否可用。
func ModelByID(name string) (Model, bool) {
	needle := strings.TrimSpace(strings.ToLower(name))
	if needle == "" {
		return Model{}, false
	}
	for _, m := range KnownModels {
		if needle == m.ID || needle == m.OptionID || needle == "model."+m.ID {
			return m, true
		}
	}
	bare := strings.TrimPrefix(needle, "model.")
	if bare == "" {
		return Model{}, false
	}
	return Model{OptionID: "model." + bare, ID: bare, Title: bare}, true
}

// Credential 是一份 today 账号凭证（落盘为 JSON）。
type Credential struct {
	UID       string `json:"uid"`
	Provider  string `json:"provider"`
	Realm     string `json:"realm"` // global / cn
	Label     string `json:"label,omitempty"`
	Session   string `json:"session"`              // Better Auth session cookie 值
	CookieRaw string `json:"cookie_raw,omitempty"` // 完整 Cookie 头，优先使用
	UserID    string `json:"user_id,omitempty"`
	Plan      string `json:"plan,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	Type      string `json:"type,omitempty"`
}

// RealmBase 返回该域名的三个基址。
func RealmBase(realm string) (app, api, auth string) {
	if strings.EqualFold(realm, "cn") {
		return CNAppBase, CNAPIBase, CNAuthBase
	}
	return GlobalAppBase, GlobalAPIBase, GlobalAuthBase
}

// Client 持有一份凭证的运行时状态。
type Client struct {
	Cred       Credential
	AppBase    string
	APIBase    string
	AuthBase   string
	jwt        string
	jwtExpires time.Time

	// 已确认的上游模型选项；避免每次请求都多打一次 pricing/status。
	modelMu       sync.Mutex
	modelOptionID string
	modelChecked  time.Time
}

// NewClient 构造一个客户端。
func NewClient(cred Credential) *Client {
	app, api, auth := RealmBase(cred.Realm)
	return &Client{Cred: cred, AppBase: app, APIBase: api, AuthBase: auth}
}

// NewClientWithBase 允许把 app/api 基址指向自建上游（mock 或中转）。
func NewClientWithBase(cred Credential, appBase, apiBase string) *Client {
	c := NewClient(cred)
	if strings.TrimSpace(appBase) != "" {
		c.AppBase = strings.TrimRight(appBase, "/")
	}
	if strings.TrimSpace(apiBase) != "" {
		c.APIBase = strings.TrimRight(apiBase, "/")
	}
	return c
}

func newID(prefix string) string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return prefix + hex.EncodeToString(buf)
}

// RandomID 返回一个 24 位十六进制随机串（用于 chatcmpl- 前缀）。
func RandomID() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (c *Client) cookieHeader() string {
	if strings.TrimSpace(c.Cred.CookieRaw) != "" {
		return strings.TrimSpace(c.Cred.CookieRaw)
	}
	if strings.TrimSpace(c.Cred.Session) == "" {
		return ""
	}
	return "__Secure-better-auth.session_token=" + strings.TrimSpace(c.Cred.Session)
}

// Token 换出可用的 API JWT（带缓存，提前 60s 续期）。
func (c *Client) Token(ctx context.Context) (string, error) {
	if c.jwt != "" && time.Now().Before(c.jwtExpires.Add(-60*time.Second)) {
		return c.jwt, nil
	}
	cookie := c.cookieHeader()
	if cookie == "" {
		return "", core.NewPluginError("missing_credential", "today credential has no session cookie", http.StatusUnauthorized)
	}
	body := map[string]any{"audience": c.APIBase, "expiresIn": 3600}
	req, errNew := http.NewRequestWithContext(ctx, http.MethodPost, c.AppBase+"/api/token", nil)
	if errNew != nil {
		return "", errNew
	}
	resp, errDo := hostbridge.DoJSON(ctx, http.MethodPost, c.AppBase+"/api/token",
		map[string]string{"Cookie": cookie}, body)
	_ = req
	if errDo != nil {
		return "", &core.UpstreamError{Kind: "network", Msg: errDo.Error()}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", &core.UpstreamError{Kind: "auth", Msg: "today session cookie rejected: " + snippet(resp.Body), Status: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		return "", &core.UpstreamError{Kind: "server", Msg: fmt.Sprintf("token exchange failed: HTTP %d %s", resp.StatusCode, snippet(resp.Body)), Status: resp.StatusCode}
	}
	var parsed struct {
		Token     string `json:"token"`
		ExpiresIn int    `json:"expiresIn"`
	}
	if errUnmarshal := json.Unmarshal(resp.Body, &parsed); errUnmarshal != nil || parsed.Token == "" {
		return "", &core.UpstreamError{Kind: "server", Msg: "token response missing token field: " + snippet(resp.Body)}
	}
	ttl := parsed.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	c.jwt = parsed.Token
	c.jwtExpires = time.Now().Add(time.Duration(ttl) * time.Second)
	return c.jwt, nil
}

func (c *Client) api(ctx context.Context, method, path string, body any) (*hostbridge.Response, error) {
	token, errToken := c.Token(ctx)
	if errToken != nil {
		return nil, errToken
	}
	resp, errDo := hostbridge.DoJSON(ctx, method, c.APIBase+path,
		map[string]string{"Authorization": "Bearer " + token}, body)
	if errDo != nil {
		return nil, &core.UpstreamError{Kind: "network", Msg: errDo.Error()}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// JWT 过期或凭证失效：强制重换一次。
		c.jwt = ""
		token, errRetry := c.Token(ctx)
		if errRetry != nil {
			return nil, errRetry
		}
		resp, errDo = hostbridge.DoJSON(ctx, method, c.APIBase+path,
			map[string]string{"Authorization": "Bearer " + token}, body)
		if errDo != nil {
			return nil, &core.UpstreamError{Kind: "network", Msg: errDo.Error()}
		}
	}
	return resp, nil
}

// Me 拉取当前用户信息。
func (c *Client) Me(ctx context.Context) (map[string]any, error) {
	resp, errAPI := c.api(ctx, http.MethodGet, "/v1/users/me", nil)
	if errAPI != nil {
		return nil, errAPI
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classify(resp, "users/me")
	}
	var out map[string]any
	if errUnmarshal := json.Unmarshal(resp.Body, &out); errUnmarshal != nil {
		return nil, &core.UpstreamError{Kind: "server", Msg: "decode users/me: " + errUnmarshal.Error()}
	}
	return out, nil
}

// PricingStatus 拉取套餐与模型选项。
func (c *Client) PricingStatus(ctx context.Context) (map[string]any, error) {
	resp, errAPI := c.api(ctx, http.MethodGet, "/v1/pricing/status", nil)
	if errAPI != nil {
		return nil, errAPI
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classify(resp, "pricing/status")
	}
	var wrapper struct {
		Data struct {
			Status map[string]any `json:"status"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(resp.Body, &wrapper); errUnmarshal != nil {
		return nil, &core.UpstreamError{Kind: "server", Msg: "decode pricing/status: " + errUnmarshal.Error()}
	}
	return wrapper.Data.Status, nil
}

// SelectedModel 读取当前模型选项。
func (c *Client) SelectedModel(ctx context.Context) (string, error) {
	status, errStatus := c.PricingStatus(ctx)
	if errStatus != nil {
		return "", errStatus
	}
	options, _ := status["modelOptions"].(map[string]any)
	if options == nil {
		return "", nil
	}
	selected, _ := options["selectedOptionId"].(string)
	return selected, nil
}

// SetModel 切换模型选项。
func (c *Client) SetModel(ctx context.Context, optionID string) error {
	resp, errAPI := c.api(ctx, http.MethodPut, "/v1/pricing/model-option", map[string]any{"optionId": optionID})
	if errAPI != nil {
		return errAPI
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return classify(resp, "pricing/model-option")
	}
	return nil
}

// Send 发送一条用户消息，返回 threadId。
func (c *Client) Send(ctx context.Context, text, threadID string) (string, error) {
	body := map[string]any{
		"content":         text,
		"messageType":     "text",
		"clientMessageId": newID("msg_"),
		// channel 必填：缺了它消息不进主对话，后续
		// GET /v1/conversations/messages?isMainChat=true 永远查不到这条 thread。
		"channel": "main",
	}
	if threadID != "" {
		body["threadId"] = threadID
	}
	resp, errAPI := c.api(ctx, http.MethodPost, "/v1/messages", body)
	if errAPI != nil {
		return "", errAPI
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return "", classify(resp, "messages")
	}
	var sent struct {
		ThreadID  string `json:"threadId"`
		MessageID string `json:"messageId"`
	}
	if errUnmarshal := json.Unmarshal(resp.Body, &sent); errUnmarshal != nil {
		return "", &core.UpstreamError{Kind: "server", Msg: "decode messages: " + snippet(resp.Body)}
	}
	if sent.ThreadID == "" {
		return "", &core.UpstreamError{Kind: "server", Msg: "messages response missing threadId: " + snippet(resp.Body)}
	}
	return sent.ThreadID, nil
}

// ConversationMessage 是 /v1/conversations/messages 里的一条。
type ConversationMessage struct {
	ThreadID string `json:"threadId"`
	Role     string `json:"role"`
	State    string `json:"state"`
	IsFinal  bool   `json:"isFinal"`
	Body     struct {
		Parts []struct {
			MessageType string `json:"messageType"`
			Content     struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"parts"`
	} `json:"body"`
}

// Text 拼接正文。
func (m ConversationMessage) Text() string {
	var buf strings.Builder
	for _, p := range m.Body.Parts {
		if p.MessageType == "text" {
			buf.WriteString(p.Content.Text)
		}
	}
	return buf.String()
}

func (c *Client) listMessages(ctx context.Context, limit int) ([]ConversationMessage, error) {
	path := fmt.Sprintf("/v1/conversations/messages?isMainChat=true&limit=%d", limit)
	resp, errAPI := c.api(ctx, http.MethodGet, path, nil)
	if errAPI != nil {
		return nil, errAPI
	}
	if resp.StatusCode != http.StatusOK {
		return nil, classify(resp, "conversations/messages")
	}
	var wrapper struct {
		Data struct {
			Items []ConversationMessage `json:"items"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(resp.Body, &wrapper); errUnmarshal != nil {
		return nil, &core.UpstreamError{Kind: "server", Msg: "decode conversations/messages: " + errUnmarshal.Error()}
	}
	return wrapper.Data.Items, nil
}

// Reply 轮询直到该 thread 的 assistant 消息完成。
//
// onDelta 会在正文增长时被调用（用于伪流式下发）。
func (c *Client) Reply(ctx context.Context, threadID string, timeout time.Duration, onDelta func(string)) (string, error) {
	deadline := time.Now().Add(timeout)
	last := ""
	// 上游每次轮询本身就要 1.5-2.7s，等待间隔必须短：
	// 间隔过长会让总耗时被"请求时间 + 等待时间"叠加拖到几十秒。
	interval := 200 * time.Millisecond
	for {
		if time.Now().After(deadline) {
			if last != "" {
				return last, nil
			}
			return "", &core.UpstreamError{Kind: "server", Msg: fmt.Sprintf("today upstream did not answer within %s", timeout)}
		}
		items, errList := c.listMessages(ctx, 3)
		if errList != nil {
			if ctx.Err() != nil {
				return last, ctx.Err()
			}
			return "", errList
		}
		for _, m := range items {
			if m.ThreadID != threadID || m.Role != "assistant" {
				continue
			}
			text := m.Text()
			if text == "" {
				continue
			}
			if onDelta != nil && len(text) > len(last) && strings.HasPrefix(text, last) {
				onDelta(text[len(last):])
			}
			last = text
			if m.State == "completed" || m.IsFinal {
				return text, nil
			}
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(interval):
		}
		if interval < time.Second {
			interval += 100 * time.Millisecond
		}
	}
}

func classify(resp *hostbridge.Response, op string) error {
	msg := fmt.Sprintf("%s failed: HTTP %d %s", op, resp.StatusCode, snippet(resp.Body))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &core.UpstreamError{Kind: "auth", Msg: msg, Status: resp.StatusCode}
	case http.StatusPaymentRequired:
		return &core.UpstreamError{Kind: "quota", Msg: msg, Status: resp.StatusCode}
	case http.StatusTooManyRequests:
		return &core.UpstreamError{Kind: "rate_limit", Msg: msg, Status: resp.StatusCode}
	}
	if resp.StatusCode >= 500 {
		return &core.UpstreamError{Kind: "server", Msg: msg, Status: resp.StatusCode}
	}
	return &core.UpstreamError{Kind: "bad_request", Msg: msg, Status: resp.StatusCode}
}

func snippet(body []byte) string {
	const limit = 240
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > limit {
		return trimmed[:limit] + "..."
	}
	return trimmed
}

// ---- 模型选择缓存（供 executor 跨请求复用） ----

// LockModelCache / UnlockModelCache 串行化同一凭证上的模型切换。
func (c *Client) LockModelCache()   { c.modelMu.Lock() }
func (c *Client) UnlockModelCache() { c.modelMu.Unlock() }

// ModelOptionID 返回已确认的上游模型选项。
func (c *Client) ModelOptionID() string { return c.modelOptionID }

// ModelCheckedRecently 报告模型状态是否仍在缓存有效期内。
func (c *Client) ModelCheckedRecently() bool {
	return c.modelOptionID != "" && time.Since(c.modelChecked) < 10*time.Minute
}

// MarkModelChecked 记录一次模型状态确认。
func (c *Client) MarkModelChecked(optionID string) {
	c.modelOptionID = optionID
	c.modelChecked = time.Now()
}
