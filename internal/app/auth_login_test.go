package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"cpa-today-plugin/cpasdk/pluginabi"
	"cpa-today-plugin/cpasdk/pluginapi"
	"cpa-today-plugin/internal/hostbridge"
)

func withMockHost(t *testing.T, fn hostbridge.Caller) {
	t.Helper()
	hostbridge.ResetForInit()
	hostbridge.Configure(fn)
	t.Cleanup(func() { hostbridge.Configure(nil) })
}

func invokeConsoleLogin(t *testing.T, a *App, body map[string]any) (int, map[string]any) {
	t.Helper()
	rawBody, _ := json.Marshal(body)
	reqBytes, _ := json.Marshal(pluginapi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/plugins/today/accounts/login",
		Body:   rawBody,
	})
	rawResp, err := a.HandleMethod(pluginabi.MethodManagementHandle, reqBytes)
	if err != nil {
		t.Fatalf("HandleMethod error: %v", err)
	}
	var env struct {
		OK     bool       `json:"ok"`
		Result mgmtResult `json:"result"`
	}
	if err := json.Unmarshal(rawResp, &env); err != nil || !env.OK {
		t.Fatalf("unexpected envelope: %s (err=%v)", rawResp, err)
	}
	var payload map[string]any
	_ = json.Unmarshal(env.Result.Body, &payload)
	return env.Result.StatusCode, payload
}

type mgmtResult struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

// Issue #1 核心复现：用户粘贴带末尾 `=` 的 URL 解码 Cookie 且未切换「国内版 todayai.cn」区域。
func TestConsoleLoginURLDecodedCookieAutoSwitchesRealm(t *testing.T) {
	const rawDecoded = "Z3spyYuKdaOA3tIcTx76T8ER6Pf49gRJ.QyWB1ypQbxCZPX6UyslmZDBm+iMJ25gubptQjs4P6Ic="
	const wantWire = "__Secure-better-auth.session_token=Z3spyYuKdaOA3tIcTx76T8ER6Pf49gRJ.QyWB1ypQbxCZPX6UyslmZDBm%2BiMJ25gubptQjs4P6Ic%3D"

	var savedName string
	var savedCred map[string]any
	var seenCookies []string

	withMockHost(t, func(callbackID, method string, payload any) (json.RawMessage, error) {
		raw, _ := json.Marshal(payload)
		switch method {
		case pluginabi.MethodHostHTTPDo:
			var req struct {
				URL     string              `json:"url"`
				Headers map[string][]string `json:"headers"`
			}
			_ = json.Unmarshal(raw, &req)
			cookieHdr := ""
			if vals := req.Headers["Cookie"]; len(vals) > 0 {
				cookieHdr = vals[0]
			}
			switch req.URL {
			case "https://today.ai/api/token":
				seenCookies = append(seenCookies, cookieHdr)
				return json.RawMessage(`{"status_code":401,"body":"eyJlcnJvciI6IlVuYXV0aG9yaXplZCJ9"}`), nil
			case "https://todayai.cn/api/token":
				seenCookies = append(seenCookies, cookieHdr)
				return json.RawMessage(`{"status_code":200,"body":"eyJ0b2tlbiI6ImNuLWp3dCIsImV4cGlyZXNJbiI6MzYwMH0="}`), nil
			case "https://api.todayai.cn/v1/users/me":
				return json.RawMessage(`{"status_code":200,"body":"eyJpZCI6InVfY24iLCJlbWFpbCI6ImNuQGV4YW1wbGUuY29tIiwicGxhbiI6InBybyJ9"}`), nil
			}
			t.Fatalf("unexpected URL: %s", req.URL)
		case pluginabi.MethodHostAuthSave:
			var save struct {
				Name string         `json:"name"`
				JSON map[string]any `json:"json"`
			}
			_ = json.Unmarshal(raw, &save)
			savedName = save.Name
			savedCred = save.JSON
			return json.RawMessage(`{}`), nil
		case pluginabi.MethodHostLog:
			return json.RawMessage(`{}`), nil
		}
		t.Fatalf("unexpected host method: %s", method)
		return nil, nil
	})

	a := New("0.1.2")
	status, data := invokeConsoleLogin(t, a, map[string]any{
		"cookie": rawDecoded,
		"realm":  "global", // 默认未切到 cn
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, data = %v", status, data)
	}
	if data["realm"] != "cn" {
		t.Fatalf("realm = %v, want cn", data["realm"])
	}
	if data["label"] != "Today-cn@example.com" {
		t.Fatalf("label = %v", data["label"])
	}
	if len(seenCookies) != 2 || seenCookies[0] != wantWire || seenCookies[1] != wantWire {
		t.Fatalf("seenCookies = %q, want both %q", seenCookies, wantWire)
	}
	if savedName == "" || savedCred["realm"] != "cn" {
		t.Fatalf("saved %q = %v", savedName, savedCred)
	}
}

// 当 Cookie 确实过期失效时，管理登录接口应返回 400 与中文诊断，而非 502。
func TestConsoleLoginRejectedCookieReturns400Not502(t *testing.T) {
	withMockHost(t, func(callbackID, method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostHTTPDo {
			return json.RawMessage(`{"status_code":401,"body":"eyJlcnJvciI6IlVuYXV0aG9yaXplZCJ9"}`), nil
		}
		return json.RawMessage(`{}`), nil
	})

	a := New("0.1.2")
	status, data := invokeConsoleLogin(t, a, map[string]any{
		"cookie": "tok.0123456789abcdef0123456789abcdef0123456789a=",
		"realm":  "global",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (data=%v)", status, data)
	}
	errMsg, _ := data["error"].(string)
	if !strings.Contains(errMsg, "已被上游拒绝") {
		t.Fatalf("error = %q, want diagnostic message", errMsg)
	}
}

// 真正的上游 5xx 故障仍应返回 502。
func TestConsoleLoginUpstream5xxReturns502(t *testing.T) {
	withMockHost(t, func(callbackID, method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostHTTPDo {
			return json.RawMessage(`{"status_code":503,"body":"c2VydmljZSB1bmF2YWlsYWJsZQ=="}`), nil
		}
		return json.RawMessage(`{}`), nil
	})

	a := New("0.1.2")
	status, _ := invokeConsoleLogin(t, a, map[string]any{
		"cookie": "tok.0123456789abcdef0123456789abcdef0123456789a=",
		"realm":  "global",
	})
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
}

// 配置了 app_base / api_base 时，控制台登录应走自定义基址而非写死公网域名。
func TestConsoleLoginHonorsConfigBaseOverrides(t *testing.T) {
	var hitURLs []string
	withMockHost(t, func(callbackID, method string, payload any) (json.RawMessage, error) {
		raw, _ := json.Marshal(payload)
		if method == pluginabi.MethodHostHTTPDo {
			var req struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(raw, &req)
			hitURLs = append(hitURLs, req.URL)
			if strings.HasSuffix(req.URL, "/api/token") {
				return json.RawMessage(`{"status_code":200,"body":"eyJ0b2tlbiI6Im1vY2stand0IiwiZXhwaXJlc0luIjozNjAwfQ=="}`), nil
			}
			return json.RawMessage(`{"status_code":200,"body":"eyJpZCI6InUxIiwiZW1haWwiOiJtb2NrQGV4YW1wbGUuY29tIn0="}`), nil
		}
		return json.RawMessage(`{}`), nil
	})

	a := New("0.1.2")
	cfgReq, _ := json.Marshal(lifecycleRequest{
		ConfigYAML: []byte("app_base: http://127.0.0.1:18999\napi_base: http://127.0.0.1:18999\n"),
	})
	if _, err := a.HandleMethod(pluginabi.MethodPluginReconfigure, cfgReq); err != nil {
		t.Fatalf("reconfigure failed: %v", err)
	}

	status, data := invokeConsoleLogin(t, a, map[string]any{"cookie": "mock-session"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, data = %v", status, data)
	}
	if len(hitURLs) == 0 || hitURLs[0] != "http://127.0.0.1:18999/api/token" {
		t.Fatalf("hitURLs = %v, want mock base", hitURLs)
	}
}

// handleAuthLoginStart 的 email-otp 必须打向 auth.<domain>，且国内版 email_sign_in_disabled 返回友好提示。
func TestAuthLoginStartEmailOTPRoutesToAuthHostAndExplainsCNDisabled(t *testing.T) {
	var gotURL string
	withMockHost(t, func(callbackID, method string, payload any) (json.RawMessage, error) {
		raw, _ := json.Marshal(payload)
		if method == pluginabi.MethodHostHTTPDo {
			var req struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(raw, &req)
			gotURL = req.URL
			body := `{"code":"auth.email_sign_in_disabled","message":"Email sign-in is unavailable in this region."}`
			resp, _ := json.Marshal(map[string]any{"status_code": 403, "body": []byte(body)})
			return resp, nil
		}
		return json.RawMessage(`{}`), nil
	})

	a := New("0.1.2")
	reqBytes, _ := json.Marshal(pluginapi.AuthLoginStartRequest{
		Metadata: map[string]any{"method": "email-otp", "realm": "cn", "email": "u@example.com"},
	})
	_, err := a.HandleMethod(pluginabi.MethodAuthLoginStart, reqBytes)
	if err == nil {
		t.Fatal("expected error when CN email OTP is disabled")
	}
	if gotURL != "https://auth.todayai.cn/api/auth/email-otp/send-verification-otp" {
		t.Fatalf("gotURL = %q, want https://auth.todayai.cn/...", gotURL)
	}
	if !strings.Contains(err.Error(), "国内版 todayai.cn 已停用邮箱验证码登录") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
