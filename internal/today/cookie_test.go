package today

import (
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeCookieInputFormats(t *testing.T) {
	const (
		decodedSlash = "NnZkFtMNXlCcAD-NNGyRSE7xPeeLfs8r.lNN7Fn2anDZKTnnze3j/fvhjoJr7Y/kH9wmhFnhhDT4="
		encodedSlash = "NnZkFtMNXlCcAD-NNGyRSE7xPeeLfs8r.lNN7Fn2anDZKTnnze3j%2FfvhjoJr7Y%2FkH9wmhFnhhDT4%3D"
		decodedPlus  = "Z3spyYuKdaOA3tIcTx76T8ER6Pf49gRJ.QyWB1ypQbxCZPX6UyslmZDBm+iMJ25gubptQjs4P6Ic="
		encodedPlus  = "Z3spyYuKdaOA3tIcTx76T8ER6Pf49gRJ.QyWB1ypQbxCZPX6UyslmZDBm%2BiMJ25gubptQjs4P6Ic%3D"
	)

	cases := []struct {
		name      string
		input     string
		wantHdr   string
		wantRealm string
	}{
		{
			name:    "bare url-decoded value with slash and trailing =",
			input:   decodedSlash,
			wantHdr: SessionCookieName + "=" + encodedSlash,
		},
		{
			name:    "bare url-decoded value with plus and trailing = (no slash)",
			input:   decodedPlus,
			wantHdr: SessionCookieName + "=" + encodedPlus,
		},
		{
			name:    "bare already url-encoded value stays idempotent",
			input:   encodedPlus,
			wantHdr: SessionCookieName + "=" + encodedPlus,
		},
		{
			name:    "quoted url-decoded value",
			input:   `"` + decodedPlus + `"`,
			wantHdr: SessionCookieName + "=" + encodedPlus,
		},
		{
			name:    "Cookie: header prefix stripped and session value encoded",
			input:   "Cookie: i18next=zh; __Secure-better-auth.session_token=" + decodedPlus,
			wantHdr: "i18next=zh; " + SessionCookieName + "=" + encodedPlus,
		},
		{
			name:    "lowercase http2 cookie: header prefix stripped",
			input:   "cookie: __Secure-better-auth.session_token=" + encodedSlash,
			wantHdr: SessionCookieName + "=" + encodedSlash,
		},
		{
			name:      "Set-Cookie header strips attributes and detects cn realm",
			input:     "Set-Cookie: __Secure-better-auth.session_token=" + encodedPlus + "; Max-Age=604800; Domain=.todayai.cn; Path=/; HttpOnly; Secure; SameSite=Lax",
			wantHdr:   SessionCookieName + "=" + encodedPlus,
			wantRealm: "cn",
		},
		{
			name:      "DevTools Application Cookies row copy (tab separated)",
			input:     "__Secure-better-auth.session_token\t" + decodedPlus + "\t.todayai.cn\t/\t2026-12-03T07:00:00.000Z\t90\t✓\t✓\tLax",
			wantHdr:   SessionCookieName + "=" + encodedPlus,
			wantRealm: "cn",
		},
		{
			name:      "multiline raw HTTP request headers",
			input:     "POST /api/token HTTP/2\r\nHost: today.ai\r\nCookie: __Secure-better-auth.session_token=" + decodedSlash + "; i18next=en\r\nUser-Agent: Mozilla/5.0",
			wantHdr:   SessionCookieName + "=" + encodedSlash + "; i18next=en",
			wantRealm: "global",
		},
		{
			name:      "Copy as cURL bash",
			input:     "curl 'https://todayai.cn/api/token' \\\n  -H 'accept: application/json' \\\n  -b '__Secure-better-auth.session_token=" + encodedPlus + "; i18next=zh'",
			wantHdr:   SessionCookieName + "=" + encodedPlus + "; i18next=zh",
			wantRealm: "cn",
		},
		{
			name:      "Copy as cURL Windows cmd with caret escapes",
			input:     "curl ^\"https://todayai.cn/api/token^\" ^\r\n  -H ^\"cookie: __Secure-better-auth.session_token=" + strings.ReplaceAll(encodedPlus, "%", "^%") + "^\"",
			wantHdr:   SessionCookieName + "=" + encodedPlus,
			wantRealm: "cn",
		},
		{
			name:      "Copy as fetch headers object",
			input:     `fetch("https://today.ai/api/token", { "headers": { "cookie": "__Secure-better-auth.session_token=` + encodedSlash + `" } });`,
			wantHdr:   SessionCookieName + "=" + encodedSlash,
			wantRealm: "global",
		},
		{
			name:      "Cookie-Editor JSON export",
			input:     `[{"domain":".todayai.cn","name":"__Secure-better-auth.session_token","value":"` + decodedPlus + `"}]`,
			wantHdr:   SessionCookieName + "=" + encodedPlus,
			wantRealm: "cn",
		},
		{
			name:    "arbitrary non-better-auth pairs preserved",
			input:   "a=1; b=2",
			wantHdr: "a=1; b=2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHdr, gotRealm := NormalizeCookieInput(tc.input)
			if gotHdr != tc.wantHdr {
				t.Fatalf("NormalizeCookieInput header:\n got: %q\nwant: %q", gotHdr, tc.wantHdr)
			}
			if gotRealm != tc.wantRealm {
				t.Fatalf("NormalizeCookieInput realm = %q, want %q", gotRealm, tc.wantRealm)
			}
		})
	}
}

func TestCookiesFromHeadersCaseInsensitiveAndSkipsCleared(t *testing.T) {
	h := http.Header{}
	// 模拟宿主 JSON 直接反序列化产生的非标准大小写 key 以及清除旧项的 Max-Age=0。
	h["set-cookie"] = []string{
		"better-auth.session_token=; Max-Age=0; Path=/",
		"__Secure-better-auth.session_token=tok123.sig+abc/def=; Max-Age=604800; Domain=.today.ai; Path=/; HttpOnly; Secure",
	}
	got := CookiesFromHeaders(h)
	want := "__Secure-better-auth.session_token=tok123.sig%2Babc%2Fdef%3D"
	if got != want {
		t.Fatalf("CookiesFromHeaders = %q, want %q", got, want)
	}
}

func TestDiagnoseCookieFailureMessages(t *testing.T) {
	if msg := DiagnoseCookieFailure("i18next=zh"); !strings.Contains(msg, "未在粘贴内容中找到") {
		t.Fatalf("unexpected missing-cookie message: %q", msg)
	}
	if msg := DiagnoseCookieFailure("__Secure-better-auth.session_token=unsigned_only"); !strings.Contains(msg, "缺少签名段") {
		t.Fatalf("unexpected unsigned-cookie message: %q", msg)
	}
	if msg := DiagnoseCookieFailure("__Secure-better-auth.session_token=tok.sig%3D"); !strings.Contains(msg, "已被上游拒绝") {
		t.Fatalf("unexpected expired-cookie message: %q", msg)
	}
}
