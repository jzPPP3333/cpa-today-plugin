package app

// management：插件自有管理路由。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"cpa-today-plugin/cpasdk/pluginabi"
	"cpa-today-plugin/cpasdk/pluginapi"
	"cpa-today-plugin/internal/core"
	"cpa-today-plugin/internal/hostbridge"
	"cpa-today-plugin/internal/today"
)

type managementRoute struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"path"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

func (a *App) managementRegistration() map[string]any {
	prefix := "/plugins/" + PluginID
	return map[string]any{
		"routes": []managementRoute{
			{Method: "GET", Path: prefix + "/status", Description: "Today 插件状态"},
			{Method: "GET", Path: prefix + "/accounts", Description: "Today 账号清单"},
			{Method: "POST", Path: prefix + "/accounts/login", Description: "登录 Today 账号（粘贴 Cookie 或邮箱 OTP）"},
			{Method: "POST", Path: prefix + "/accounts/delete", Description: "删除 Today 账号"},
			{Method: "POST", Path: prefix + "/chat", Description: "直连测试一次对话"},
		},
		"resources": []resourceRoute{
			{Path: "/console", Menu: DisplayName, Description: "Today 账号与控制台"},
		},
	}
}

type managementRPC struct {
	Method  string              `json:"Method"`
	Path    string              `json:"Path"`
	Headers http.Header         `json:"Headers"`
	Query   map[string][]string `json:"Query"`
	Body    []byte              `json:"Body"`
}

func (a *App) handleManagement(request []byte) ([]byte, error) {
	var rpc managementRPC
	if len(request) > 0 {
		// 宿主用的是 pluginapi.ManagementRequest 的 Go 字段名（无 json tag）。
		var raw pluginapi.ManagementRequest
		if errUnmarshal := json.Unmarshal(request, &raw); errUnmarshal == nil {
			rpc.Method = raw.Method
			rpc.Path = raw.Path
			rpc.Headers = raw.Headers
			rpc.Query = raw.Query
			rpc.Body = raw.Body
		}
	}
	method := strings.ToUpper(strings.TrimSpace(rpc.Method))
	if method == "" {
		method = http.MethodGet
	}
	path := strings.TrimSpace(rpc.Path)
	prefix := "/plugins/" + PluginID
	if idx := strings.Index(path, prefix); idx > 0 {
		path = path[idx:]
	}

	switch {
	case method == http.MethodGet && strings.HasSuffix(path, "/console"):
		// 禁缓存：改版后浏览器仍拿旧页面会让按钮看起来"没反应"。
		resp := managementResponse(http.StatusOK, "text/html; charset=utf-8", []byte(consoleHTML(prefix)))
		resp.Headers.Set("Cache-Control", "no-store, no-cache, must-revalidate")
		resp.Headers.Set("Pragma", "no-cache")
		return core.OKEnvelope(resp)
	case method == http.MethodGet && strings.HasSuffix(path, "/status"):
		return core.OKEnvelope(a.statusPayload())
	case method == http.MethodGet && strings.HasSuffix(path, "/accounts"):
		return core.OKEnvelope(a.accountsPayload())
	case method == http.MethodPost && strings.HasSuffix(path, "/accounts/login"):
		return a.handleConsoleLogin(rpc)
	case method == http.MethodPost && strings.HasSuffix(path, "/accounts/delete"):
		return a.handleConsoleDelete(rpc)
	case method == http.MethodPost && strings.HasSuffix(path, "/chat"):
		return a.handleConsoleChat(rpc)
	}
	return core.OKEnvelope(managementResponse(http.StatusNotFound, "application/json", []byte(`{"error":"not found"}`)))
}

type mgmtResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

func managementResponse(status int, contentType string, body []byte) mgmtResponse {
	return mgmtResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{contentType}},
		Body:       body,
	}
}

func jsonResponse(status int, value any) mgmtResponse {
	encoded, _ := json.Marshal(value)
	return managementResponse(status, "application/json; charset=utf-8", encoded)
}

func (a *App) statusPayload() mgmtResponse {
	cfg := a.Config()
	return jsonResponse(http.StatusOK, map[string]any{
		"plugin":          PluginID,
		"version":         a.version,
		"provider":        ProviderKey,
		"default_model":   cfg.DefaultModel,
		"timeout_seconds": cfg.TimeoutSeconds,
		"models":          modelNames(a),
	})
}

func modelNames(a *App) []string {
	ids := a.ProbedModels()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, PublicModelID(id))
	}
	return out
}

// accountsPayload 列出宿主里属于本插件的凭证及其实时状态。
func (a *App) accountsPayload() mgmtResponse {
	entries, errList := hostbridge.ListAuths(context.Background())
	if errList != nil {
		return jsonResponse(http.StatusBadGateway, map[string]any{"error": errList.Error()})
	}
	type account struct {
		Name    string `json:"name"`
		Label   string `json:"label"`
		Realm   string `json:"realm"`
		Plan    string `json:"plan,omitempty"`
		Healthy bool   `json:"healthy"`
		Error   string `json:"error,omitempty"`
	}
	out := make([]account, 0, len(entries))
	for _, e := range entries {
		if !strings.EqualFold(e.Provider, ProviderKey) && !strings.HasPrefix(strings.ToLower(e.Name), "today-") {
			continue
		}
		out = append(out, account{Name: e.Name, Label: e.Label, Healthy: !e.Disabled})
	}
	return jsonResponse(http.StatusOK, map[string]any{"accounts": out, "count": len(out)})
}

// handleConsoleLogin 处理控制台提交的登录（Cookie 或邮箱验证码）。
func (a *App) handleConsoleLogin(rpc managementRPC) ([]byte, error) {
	var body struct {
		Cookie string `json:"cookie"`
		Email  string `json:"email"`
		OTP    string `json:"otp"`
		Realm  string `json:"realm"`
	}
	if len(rpc.Body) > 0 {
		if errUnmarshal := json.Unmarshal(rpc.Body, &body); errUnmarshal != nil {
			return core.OKEnvelope(jsonResponse(http.StatusBadRequest, map[string]any{"error": "invalid JSON: " + errUnmarshal.Error()}))
		}
	}
	realm := strings.ToLower(strings.TrimSpace(body.Realm))
	if realm != "cn" {
		realm = "global"
	}

	if strings.TrimSpace(body.Cookie) == "" && strings.TrimSpace(body.Email) != "" && strings.TrimSpace(body.OTP) == "" {
		// 第一阶段：发验证码。
		_, _, authBase := today.RealmBase(realm)
		resp, errDo := hostbridge.DoJSON(context.Background(), http.MethodPost,
			authBase+"/api/auth/email-otp/send-verification-otp",
			nil, map[string]any{"email": strings.TrimSpace(body.Email), "type": "sign-in"})
		if errDo != nil {
			return core.OKEnvelope(jsonResponse(http.StatusBadGateway, map[string]any{"error": errDo.Error()}))
		}
		if resp.StatusCode != http.StatusOK {
			return core.OKEnvelope(jsonResponse(http.StatusBadGateway, map[string]any{
				"error": fmt.Sprintf("send OTP failed: HTTP %d %.200s", resp.StatusCode, resp.Body),
			}))
		}
		return core.OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"ok": true, "stage": "otp_sent"}))
	}

	cred, errCred := loginByCookieOrOTP(body.Cookie, body.Email, body.OTP, realm)
	if errCred != nil {
		return core.OKEnvelope(jsonResponse(http.StatusBadGateway, map[string]any{"error": errCred.Error()}))
	}
	payload, _ := json.Marshal(cred)
	fileName := cred.UID + ".json"
	if errSave := hostbridge.AuthSave(context.Background(), fileName, payload); errSave != nil {
		return core.OKEnvelope(jsonResponse(http.StatusInternalServerError, map[string]any{"error": errSave.Error()}))
	}
	return core.OKEnvelope(jsonResponse(http.StatusOK, map[string]any{
		"ok": true, "file": fileName, "label": cred.Label, "realm": cred.Realm,
	}))
}

func loginByCookieOrOTP(cookie, email, otp, realm string) (today.Credential, error) {
	cred := today.Credential{Provider: ProviderKey, Realm: realm, Type: ProviderKey}
	if strings.TrimSpace(cookie) != "" {
		cred.CookieRaw = normalizeCookie(cookie)
	} else if strings.TrimSpace(email) != "" && strings.TrimSpace(otp) != "" {
		_, _, authBase := today.RealmBase(realm)
		resp, errDo := hostbridge.DoJSON(context.Background(), http.MethodPost,
			authBase+"/api/auth/sign-in/email-otp", nil,
			map[string]any{"email": strings.TrimSpace(email), "otp": strings.TrimSpace(otp)})
		if errDo != nil {
			return cred, errDo
		}
		if resp.StatusCode != http.StatusOK {
			return cred, fmt.Errorf("邮箱验证码登录失败: HTTP %d %.200s", resp.StatusCode, resp.Body)
		}
		header := cookiesFromResponse(resp.Headers)
		if header == "" {
			return cred, fmt.Errorf("登录成功但未下发 session cookie")
		}
		cred.CookieRaw = header
	} else {
		return cred, fmt.Errorf("需要 cookie 或 email+otp")
	}

	client := today.NewClient(cred)
	ctx, cancel := context.WithTimeout(context.Background(), hostbridge.DefaultTimeout)
	defer cancel()
	if _, errToken := client.Token(ctx); errToken != nil {
		return cred, fmt.Errorf("凭证验证失败: %w", errToken)
	}
	if me, errMe := client.Me(ctx); errMe == nil {
		if emailValue, okEmail := me["email"].(string); okEmail && emailValue != "" {
			cred.Label = "Today-" + emailValue
		}
		if id, okID := me["id"].(string); okID {
			cred.UserID = id
		}
	}
	cred.UID = credentialUID("", cred.Session, cred.CookieRaw)
	if cred.Label == "" {
		cred.Label = cred.UID
	}
	return cred, nil
}

func (a *App) handleConsoleDelete(rpc managementRPC) ([]byte, error) {
	var body struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(rpc.Body, &body)
	if strings.TrimSpace(body.Name) == "" {
		return core.OKEnvelope(jsonResponse(http.StatusBadRequest, map[string]any{"error": "name is required"}))
	}
	return core.OKEnvelope(jsonResponse(http.StatusOK, map[string]any{
		"ok":   true,
		"note": "请在 CPA 管理面板的凭证列表里删除该文件",
		"file": body.Name,
	}))
}

// handleConsoleChat 用当前凭证直接跑一次对话，用于排查上游问题。
func (a *App) handleConsoleChat(rpc managementRPC) ([]byte, error) {
	var body struct {
		Prompt string `json:"prompt"`
		Model  string `json:"model"`
	}
	_ = json.Unmarshal(rpc.Body, &body)
	if strings.TrimSpace(body.Prompt) == "" {
		return core.OKEnvelope(jsonResponse(http.StatusBadRequest, map[string]any{"error": "prompt is required"}))
	}
	cred, errCred := a.firstCredential()
	if errCred != nil {
		return core.OKEnvelope(jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": errCred.Error()}))
	}
	model, okModel := today.ModelByID(StripModelPrefix(body.Model))
	if !okModel {
		model, _ = today.ModelByID(StripModelPrefix(a.Config().DefaultModel))
	}
	client := today.NewClient(cred)
	ctx, cancel := context.WithTimeout(context.Background(), 3*60*1e9)
	defer cancel()
	if errModel := ensureModel(ctx, client, model); errModel != nil {
		return core.OKEnvelope(jsonResponse(http.StatusBadGateway, map[string]any{"error": errModel.Error()}))
	}
	threadID, errSend := client.Send(ctx, body.Prompt, "")
	if errSend != nil {
		return core.OKEnvelope(jsonResponse(http.StatusBadGateway, map[string]any{"error": errSend.Error()}))
	}
	reply, errReply := client.Reply(ctx, threadID, 3*60*1e9, nil)
	if errReply != nil {
		return core.OKEnvelope(jsonResponse(http.StatusBadGateway, map[string]any{"error": errReply.Error()}))
	}
	return core.OKEnvelope(jsonResponse(http.StatusOK, map[string]any{
		"ok": true, "model": PublicModelID(model.ID), "reply": reply, "thread_id": threadID,
	}))
}

var _ = pluginabi.MethodManagementHandle
