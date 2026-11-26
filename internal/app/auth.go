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
	rawRealm, _ := probe["realm"].(string)
	var cred today.Credential
	if errUnmarshal := json.Unmarshal(raw, &cred); errUnmarshal != nil {
		return today.Credential{}, false
	}
	if strings.TrimSpace(cred.CookieRaw) != "" {
		normalized, hint := today.NormalizeCookieInput(cred.CookieRaw)
		cred.CookieRaw = normalized
		if strings.TrimSpace(rawRealm) == "" && hint != "" {
			cred.Realm = hint
		}
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
	realm := normalizeRealm(strAny(rpc.Metadata["realm"]))

	state := loginState{
		Method:    method,
		Realm:     realm,
		Email:     strings.TrimSpace(strAny(rpc.Metadata["email"])),
		CreatedAt: time.Now(),
	}

	ctx := hostbridge.WithCallbackID(context.Background(), rpc.HostCallbackID)
	if method == "email-otp" {
		if state.Email == "" {
			return nil, core.NewPluginError("invalid_request", "email is required for email-otp login", http.StatusBadRequest)
		}
		if errSend := sendEmailOTP(ctx, state.Email, realm); errSend != nil {
			return nil, core.ToPluginError(errSend)
		}
	}

	appBase, _, _ := today.RealmBase(realm)
	encoded, _ := json.Marshal(state)
	return core.OKEnvelope(pluginapi.AuthLoginStartResponse{
		Provider:  ProviderKey,
		URL:       appBase + "/login",
		State:     string(encoded),
		ExpiresAt: time.Now().Add(30 * time.Minute),
		Metadata: map[string]any{
			"method": method,
			"realm":  realm,
			"hint":   loginHint(method, realm),
		},
	})
}

func loginHint(method, realm string) string {
	if method == "email-otp" {
		return "验证码已发送到邮箱；把邮件里的验证码连同邮箱一起提交。"
	}
	site := "today.ai"
	if normalizeRealm(realm) == "cn" {
		site = "todayai.cn"
	}
	return "在浏览器登录 " + site + "，F12 → Application → Cookies → 复制 __Secure-better-auth.session_token 的值（或整段 Cookie 头）。"
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
	state.Realm = normalizeRealm(state.Realm)

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
			Message: loginHint(state.Method, state.Realm),
		})
	}

	ctx := hostbridge.WithCallbackID(context.Background(), rpc.HostCallbackID)
	cred, errLogin := a.authenticateByCookieOrOTP(ctx, cookie, state.Email, code, state.Realm)
	if errLogin != nil {
		var up *core.UpstreamError
		if isUpstreamUnavailable(errLogin, &up) {
			return nil, core.NewPluginError("upstream_unreachable", errLogin.Error(), http.StatusBadGateway)
		}
		return core.OKEnvelope(pluginapi.AuthLoginPollResponse{
			Status:  pluginapi.AuthLoginStatusError,
			Message: errLogin.Error(),
		})
	}

	payload, _ := json.Marshal(cred)
	fileName := cred.UID + ".json"
	hostbridge.Log("info", "today: log in ok, writing "+fileName)
	if errSave := hostbridge.AuthSave(ctx, fileName, payload); errSave != nil {
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

func sendEmailOTP(ctx context.Context, email, realm string) error {
	appBase, _, authBase := today.RealmBase(realm)
	headers := map[string]string{
		"Origin":  appBase,
		"Referer": appBase + "/",
	}
	resp, errDo := hostbridge.DoJSON(ctx, http.MethodPost,
		authBase+"/api/auth/email-otp/send-verification-otp",
		headers, map[string]any{"email": strings.TrimSpace(email), "type": "sign-in"})
	if errDo != nil {
		return &core.UpstreamError{Kind: "network", Msg: errDo.Error()}
	}
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if strings.Contains(string(resp.Body), "email_sign_in_disabled") {
		return core.NewPluginError("email_otp_disabled",
			"国内版 todayai.cn 已停用邮箱验证码登录，请改用「粘贴浏览器 Cookie」方式登录",
			http.StatusBadRequest)
	}
	msg := fmt.Sprintf("发送验证码失败: HTTP %d %.200s", resp.StatusCode, resp.Body)
	if resp.StatusCode == http.StatusTooManyRequests {
		return core.RetryablePluginError("otp_rate_limited", msg, http.StatusTooManyRequests)
	}
	if resp.StatusCode >= 500 {
		return core.RetryablePluginError("otp_send_failed", msg, http.StatusBadGateway)
	}
	return core.NewPluginError("otp_send_failed", msg, http.StatusBadRequest)
}

func signInEmailOTP(ctx context.Context, email, otp, realm string) (string, error) {
	appBase, _, authBase := today.RealmBase(realm)
	headers := map[string]string{
		"Origin":  appBase,
		"Referer": appBase + "/",
	}
	resp, errDo := hostbridge.DoJSON(ctx, http.MethodPost,
		authBase+"/api/auth/sign-in/email-otp",
		headers, map[string]any{"email": strings.TrimSpace(email), "otp": strings.TrimSpace(otp)})
	if errDo != nil {
		return "", &core.UpstreamError{Kind: "network", Msg: errDo.Error()}
	}
	if resp.StatusCode != http.StatusOK {
		if strings.Contains(string(resp.Body), "email_sign_in_disabled") {
			return "", core.NewPluginError("email_otp_disabled",
				"国内版 todayai.cn 已停用邮箱验证码登录，请改用「粘贴浏览器 Cookie」方式登录",
				http.StatusBadRequest)
		}
		msg := fmt.Sprintf("邮箱验证码登录失败: HTTP %d %.200s", resp.StatusCode, resp.Body)
		if resp.StatusCode >= 500 {
			return "", core.RetryablePluginError("otp_verify_failed", msg, http.StatusBadGateway)
		}
		return "", core.NewPluginError("otp_verify_failed", msg, http.StatusBadRequest)
	}
	cookieHeader := today.CookiesFromHeaders(resp.Headers)
	if cookieHeader == "" {
		return "", core.NewPluginError("missing_session_cookie", "登录成功但服务端未下发 session cookie", http.StatusBadGateway)
	}
	return cookieHeader, nil
}

// authenticateByCookieOrOTP 完成 Cookie 清洗、OTP 换会话、JWT 校验与跨区域自动回退。
func (a *App) authenticateByCookieOrOTP(ctx context.Context, cookie, email, otp, realm string) (today.Credential, error) {
	realm = normalizeRealm(realm)
	cfg := a.Config()
	cred := today.Credential{Provider: ProviderKey, Realm: realm, Type: ProviderKey}

	allowRealmFallback := false
	switch {
	case strings.TrimSpace(cookie) != "":
		normalized, realmHint := today.NormalizeCookieInput(cookie)
		if normalized == "" {
			return cred, core.NewPluginError("invalid_cookie", "Cookie 内容为空或格式无法识别", http.StatusBadRequest)
		}
		cred.CookieRaw = normalized
		if realmHint != "" {
			cred.Realm = realmHint
		}
		allowRealmFallback = true
	case strings.TrimSpace(email) != "" && strings.TrimSpace(otp) != "":
		cookieHeader, errOTP := signInEmailOTP(ctx, email, otp, realm)
		if errOTP != nil {
			return cred, errOTP
		}
		cred.CookieRaw = cookieHeader
	default:
		return cred, core.NewPluginError("invalid_request", "需要提供 cookie 或 email+otp", http.StatusBadRequest)
	}

	verifyCtx, cancel := context.WithTimeout(ctx, hostbridge.DefaultTimeout)
	defer cancel()

	client := today.NewClientWithBase(cred, cfg.AppBase, cfg.APIBase)
	_, errToken := client.Token(verifyCtx)
	if errToken != nil && allowRealmFallback && strings.TrimSpace(cfg.AppBase) == "" && isUpstreamAuthRejected(errToken) {
		// 国内版 todayai.cn 与国际版 today.ai 的 Cookie 互不通用；用户在控制台忘切区域时自动尝试另一侧。
		altCred := cred
		altCred.Realm = alternateRealm(cred.Realm)
		altClient := today.NewClientWithBase(altCred, cfg.AppBase, cfg.APIBase)
		if _, errAlt := altClient.Token(verifyCtx); errAlt == nil {
			hostbridge.Log("info", "today: cookie matched alternate realm "+altCred.Realm)
			cred = altCred
			client = altClient
			errToken = nil
		}
	}
	if errToken != nil {
		if isUpstreamAuthRejected(errToken) {
			return cred, core.NewPluginError("invalid_credential",
				"凭证验证失败："+today.DiagnoseCookieFailure(cred.CookieRaw),
				http.StatusBadRequest)
		}
		return cred, fmt.Errorf("凭证验证失败: %w", errToken)
	}

	if me, errMe := client.Me(verifyCtx); errMe == nil {
		if id, okID := me["id"].(string); okID && id != "" {
			cred.UserID = id
		}
		if emailVal, okEmail := me["email"].(string); okEmail && emailVal != "" {
			cred.Label = "Today-" + emailVal
		}
		if planVal, okPlan := me["plan"].(string); okPlan && planVal != "" {
			cred.Plan = planVal
		}
	}
	cred.UID = credentialUID("", cred.Session, cred.CookieRaw)
	if cred.Label == "" {
		cred.Label = "Today-" + strings.TrimPrefix(cred.UID, "today-")
	}
	return cred, nil
}

func isUpstreamAuthRejected(err error) bool {
	if err == nil {
		return false
	}
	pe := core.ToPluginError(err)
	if pluginErr, ok := pe.(*core.PluginError); ok {
		return pluginErr.HTTPStatus == http.StatusUnauthorized || pluginErr.HTTPStatus == http.StatusForbidden
	}
	return false
}

func isUpstreamUnavailable(err error, target **core.UpstreamError) bool {
	pe := core.ToPluginError(err)
	if pluginErr, ok := pe.(*core.PluginError); ok {
		return pluginErr.Code == "upstream_network"
	}
	_ = target
	return false
}

func normalizeCookie(raw string) string {
	return today.NormalizeCookieHeader(raw)
}

func cookiesFromResponse(headers http.Header) string {
	return today.CookiesFromHeaders(headers)
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
