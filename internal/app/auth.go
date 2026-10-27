package app

// auth provider：凭证识别、登录、续期。
//
// 归属：文件名 today-*.json，或 JSON 里 provider/vendor == "today"。
// 登录：粘贴浏览器 Cookie，或邮箱 OTP（better-auth）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cpa-today-plugin/cpasdk/pluginabi"
	"cpa-today-plugin/cpasdk/pluginapi"
	"cpa-today-plugin/internal/core"
	"cpa-today-plugin/internal/hostbridge"
	"cpa-today-plugin/internal/today"
)

type authParseRPC struct {
	pluginapi.AuthParseRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authLoginStartRPC struct {
	pluginapi.AuthLoginStartRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authLoginPollRPC struct {
	pluginapi.AuthLoginPollRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authRefreshRPC struct {
	pluginapi.AuthRefreshRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// parseCredential 解析一份 today 凭证 JSON。
func parseCredential(raw []byte, fileName string) (today.Credential, bool) {
	var probe map[string]any
	if errUnmarshal := json.Unmarshal(raw, &probe); errUnmarshal != nil {
		return today.Credential{}, false
	}
	provider, _ := probe["provider"].(string)
	vendor, _ := probe["vendor"].(string)
	if !strings.EqualFold(provider, ProviderKey) && !strings.EqualFold(vendor, ProviderKey) {
		if !strings.HasPrefix(strings.ToLower(fileName), "today-") {
			return today.Credential{}, false
		}
	}
	var cred today.Credential
	if errUnmarshal := json.Unmarshal(raw, &cred); errUnmarshal != nil {
		return today.Credential{}, false
	}
	if strings.TrimSpace(cred.Session) == "" && strings.TrimSpace(cred.CookieRaw) == "" {
		return today.Credential{}, false
	}
	if cred.Provider == "" {
		cred.Provider = ProviderKey
	}
	if cred.Realm == "" {
		cred.Realm = "global"
	}
	if cred.Type == "" {
		cred.Type = ProviderKey
	}
	if cred.UID == "" {
		cred.UID = credentialUID(fileName, cred.Session, cred.CookieRaw)
	}
	if cred.Label == "" {
		cred.Label = "Today-" + cred.UID
	}
	return cred, true
}

func credentialUID(fileName, session, cookieRaw string) string {
	if strings.HasPrefix(strings.ToLower(fileName), "today-") && strings.HasSuffix(strings.ToLower(fileName), ".json") {
		return strings.TrimSuffix(fileName[:len(fileName)-len(".json")], "")
	}
	seed := session
	if strings.TrimSpace(seed) == "" {
		seed = cookieRaw
	}
	sum := sha256.Sum256([]byte(seed))
	return "today-" + hex.EncodeToString(sum[:][:6])
}

// handleAuthParse 识别并解析凭证文件。
func (a *App) handleAuthParse(request []byte) ([]byte, error) {
	var rpc authParseRPC
	if errDecode := decodeRequest(request, &rpc); errDecode != nil {
		return core.ErrorEnvelopeFromError(core.NewPluginError("invalid_request", errDecode.Error(), http.StatusBadRequest)), nil
	}
	if len(rpc.RawJSON) == 0 {
		return core.OKEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	cred, ok := parseCredential(rpc.RawJSON, rpc.FileName)
	if !ok {
		return core.OKEnvelope(pluginapi.AuthParseResponse{Handled: false})
	}
	auth := pluginapi.AuthData{
		Provider:   ProviderKey,
		ID:         cred.UID,
		FileName:   rpc.FileName,
		Label:      cred.Label,
		Disabled:   cred.Disabled,
		Metadata:   map[string]any{"realm": cred.Realm, "plan": cred.Plan},
		Attributes: map[string]string{"today_realm": cred.Realm},
	}
	auth.StorageJSON = rpc.RawJSON
	return core.OKEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: auth})
}

// handleAuthLoginStart 启动登录流程。
//
// 支持两种方式（由 metadata.method 选择）：
//   - "cookie"（默认）：用户在控制台粘贴浏览器 Cookie，随后 Poll 时直接完成；
//   - "email-otp"：向邮箱发验证码。
func (a *App) handleAuthLoginStart(request []byte) ([]byte, error) {
	var rpc authLoginStartRPC
	if errDecode := decodeRequest(request, &rpc); errDecode != nil {
		return nil, core.NewPluginError("invalid_request", errDecode.Error(), http.StatusBadRequest)
	}
	method := strings.ToLower(strings.TrimSpace(strAny(rpc.Metadata["method"])))
	if method == "" {
		method = "cookie"
	}
	realm := strings.ToLower(strings.TrimSpace(strAny(rpc.Metadata["realm"])))
	if realm != "cn" {
		realm = "global"
	}

	state := loginState{
		Method:    method,
		Realm:     realm,
		Email:     strings.TrimSpace(strAny(rpc.Metadata["email"])),
		CreatedAt: time.Now(),
	}

	if method == "email-otp" {
		if state.Email == "" {
			return nil, core.NewPluginError("invalid_request", "email is required for email-otp login", http.StatusBadRequest)
		}
		_, authBase, _ := today.RealmBase(realm)
		host := authBase
		resp, errDo := hostbridge.DoJSON(context.Background(), http.MethodPost,
			host+"/api/auth/email-otp/send-verification-otp",
			nil, map[string]any{"email": state.Email, "type": "sign-in"})
		if errDo != nil {
			return nil, core.NewPluginError("upstream_unreachable", errDo.Error(), http.StatusBadGateway)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, core.NewPluginError("otp_send_failed",
				fmt.Sprintf("send OTP failed: HTTP %d %.200s", resp.StatusCode, resp.Body), http.StatusBadGateway)
		}
	}

	encoded, _ := json.Marshal(state)
	return core.OKEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  ProviderKey,
		URL:       "https://today.ai/login",
		State:     string(encoded),
		ExpiresAt: time.Now().Add(30 * time.Minute),
		Metadata: map[string]any{
			"method": method,
			"realm":  realm,
			"hint":   loginHint(method),
		},
	})
}

func loginHint(method string) string {
	if method == "email-otp" {
		return "验证码已发送到邮箱；把邮件里的验证码连同邮箱一起提交。"
	}
	return "在浏览器登录 today.ai，F12 → Application → Cookies → today.ai，复制 __Secure-better-auth.session_token 的值（或整段 Cookie 头）。"
}

type loginState struct {
	Method    string    `json:"method"`
	Realm     string    `json:"realm"`
	Email     string    `json:"email,omitempty"`
	Code      string    `json:"code,omitempty"`
	Cookie    string    `json:"cookie,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// handleAuthLoginPoll 处理登录轮询。
func (a *App) handleAuthLoginPoll(request []byte) ([]byte, error) {
	var rpc authLoginPollRPC
	if errDecode := decodeRequest(request, &rpc); errDecode != nil {
		return nil, core.NewPluginError("invalid_request", errDecode.Error(), http.StatusBadRequest)
	}
	var state loginState
	if errUnmarshal := json.Unmarshal([]byte(rpc.State), &state); errUnmarshal != nil {
		return nil, core.NewPluginError("invalid_state", "login state is not decodable", http.StatusBadRequest)
	}
	if state.Realm == "" {
		state.Realm = "global"
	}

	cookie := strings.TrimSpace(strAny(rpc.Metadata["cookie"]))
	code := strings.TrimSpace(strAny(rpc.Metadata["code"]))
	if cookie == "" && state.Method == "cookie" {
		cookie = strings.TrimSpace(strAny(rpc.Metadata["session"]))
	}
	if code == "" && state.Method == "email-otp" {
		code = strings.TrimSpace(strAny(rpc.Metadata["otp"]))
	}

	if cookie == "" && code == "" {
		return core.OKEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusPending,
			Message: loginHint(state.Method),
		})
	}

	appBase, _, authBase := today.RealmBase(state.Realm)
	cred := today.Credential{Provider: ProviderKey, Realm: state.Realm, Type: ProviderKey}

	switch {
	case cookie != "":
		cred.CookieRaw = normalizeCookie(cookie)
		cred.Provider = ProviderKey
	case state.Method == "email-otp":
		resp, errDo := hostbridge.DoJSON(context.Background(), http.MethodPost,
			authBase+"/api/auth/sign-in/email-otp",
			nil, map[string]any{"email": state.Email, "otp": code})
		if errDo != nil {
			return nil, core.NewPluginError("upstream_unreachable", errDo.Error(), http.StatusBadGateway)
		}
		if resp.StatusCode != http.StatusOK {
			return core.OKEnvelope(pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: fmt.Sprintf("验证码登录失败: HTTP %d %.200s", resp.StatusCode, resp.Body),
			})
		}
		cookieHeader := cookiesFromResponse(resp.Headers)
		if cookieHeader == "" {
			return core.OKEnvelope(pluginapi.AuthLoginPollResponse{
				Status:  pluginapi.AuthLoginStatusError,
				Message: "登录成功但服务端未下发 session cookie",
			})
		}
		cred.CookieRaw = cookieHeader
	}

	// 用换 token 验证凭证是否真的可用。
	client := today.NewClient(cred)
	tokenCtx, cancel := context.WithTimeout(context.Background(), hostbridge.DefaultTimeout)
	defer cancel()
	if _, errToken := client.Token(tokenCtx); errToken != nil {
		return core.OKEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: "凭证验证失败：" + errToken.Error(),
		})
	}
	_ = appBase

	// 拉用户信息补全 label。
	if me, errMe := client.Me(tokenCtx); errMe == nil {
		if id, okID := me["id"].(string); okID && id != "" {
			cred.UserID = id
		}
		if email, okEmail := me["email"].(string); okEmail && email != "" {
			cred.Label = "Today-" + email
		}
	}
	if cred.Label == "" {
		sum := sha256.Sum256([]byte(cred.CookieRaw + cred.Session))
		cred.UID = "today-" + hex.EncodeToString(sum[:6])
		cred.Label = "Today-" + hex.EncodeToString(sum[:3])
	} else {
		sum := sha256.Sum256([]byte(cred.CookieRaw + cred.Session))
		cred.UID = "today-" + hex.EncodeToString(sum[:6])
	}
	if cred.UID == "" {
		cred.UID = credentialUID("", cred.Session, cred.CookieRaw)
	}

	payload, _ := json.Marshal(cred)
	fileName := cred.UID + ".json"
	hostbridge.Log("info", "today: log in ok, writing "+fileName)
	if errSave := hostbridge.AuthSave(context.Background(), fileName, payload); errSave != nil {
		return nil, core.NewPluginError("auth_save_failed", errSave.Error(), http.StatusInternalServerError)
	}

	auth := pluginapi.AuthData{
		Provider:    ProviderKey,
		ID:          cred.UID,
		FileName:    fileName,
		Label:       cred.Label,
		StorageJSON: payload,
		Metadata:    map[string]any{"realm": cred.Realm},
		Attributes:  map[string]string{"today_realm": cred.Realm},
	}
	return core.OKEnvelope(pluginapi.AuthLoginPollResponse{
		Status:  pluginapi.AuthLoginStatusSuccess,
		Message: "登录成功：" + cred.Label,
		Auth:    auth,
	})
}

func normalizeCookie(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.ReplaceAll(value, "\r", "")
	value = strings.ReplaceAll(value, "\n", " ")
	if strings.Contains(value, "=") {
		return strings.Join(strings.Fields(value), " ")
	}
	return "__Secure-better-auth.session_token=" + value
}

func cookiesFromResponse(headers http.Header) string {
	if headers == nil {
		return ""
	}
	var parts []string
	for _, cookie := range headers.Values("Set-Cookie") {
		segment := strings.SplitN(cookie, ";", 2)[0]
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		name := strings.SplitN(segment, "=", 2)[0]
		if strings.Contains(name, "session_token") || strings.Contains(name, "better-auth") {
			parts = append(parts, segment)
		}
	}
	return strings.Join(parts, "; ")
}

// handleAuthRefresh 校验凭证仍然可用并回写。
func (a *App) handleAuthRefresh(request []byte) ([]byte, error) {
	var rpc authRefreshRPC
	if errDecode := decodeRequest(request, &rpc); errDecode != nil {
		return nil, core.NewPluginError("invalid_request", errDecode.Error(), http.StatusBadRequest)
	}
	cred, ok := parseCredential(rpc.StorageJSON, rpc.AuthID+".json")
	if !ok {
		return nil, core.NewPluginError("invalid_credential", "credential is not a today credential", http.StatusBadRequest)
	}
	cfg := a.Config()
	client := clientFor(cred, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), hostbridge.DefaultTimeout)
	defer cancel()
	if _, errToken := client.Token(ctx); errToken != nil {
		return nil, core.ToPluginError(errToken)
	}
	if me, errMe := client.Me(ctx); errMe == nil {
		if plan, okPlan := me["plan"].(string); okPlan {
			cred.Plan = plan
		}
	}
	payload, _ := json.Marshal(cred)
	return core.OKEnvelope(pluginapi.AuthRefreshResponse{
		Auth: pluginapi.AuthData{
			Provider:    ProviderKey,
			ID:          cred.UID,
			FileName:    rpc.AuthID + ".json",
			Label:       cred.Label,
			StorageJSON: payload,
		},
		NextRefreshAfter: time.Now().Add(6 * time.Hour),
	})
}

func strAny(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return fmt.Sprintf("%v", t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}

var _ = pluginabi.MethodAuthParse
