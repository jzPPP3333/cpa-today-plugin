package today

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// SessionCookieName 是 Better Auth 在 HTTPS 下使用的主会话 Cookie 名。
const SessionCookieName = "__Secure-better-auth.session_token"

var (
	reCurlCookieFlag = regexp.MustCompile(`(?i)(?:^|\s)(?:-b|--cookie)(?:=|\s+)(?:'([^']+)'|"((?:\\.|[^"\\])+)"|(\S+))`)
	reCurlHeaderFlag = regexp.MustCompile(`(?i)(?:-H|--header)(?:=|\s+)(?:'(?:set-)?cookie\s*:\s*([^']+)'|"(?:set-)?cookie\s*:\s*((?:\\.|[^"\\])+)")`)
	reFetchCookieKV  = regexp.MustCompile(`(?i)["']cookie["']\s*:\s*(?:"((?:\\.|[^"\\])+)"|'((?:\\.|[^'\\])+)')`)
	rePSCookie       = regexp.MustCompile(`(?i)System\.Net\.Cookie\(\s*"([^"]+)"\s*,\s*"([^"]*)"`)
)

// NormalizeCookieHeader 把各种形态的粘贴内容规整成可直接放入 HTTP Cookie 请求头的字符串。
func NormalizeCookieHeader(raw string) string {
	header, _ := NormalizeCookieInput(raw)
	return header
}

// NormalizeCookieInput 清洗用户粘贴的 Cookie 文本，并顺带识别文本中可能出现的站点域名提示（"cn" 或 "global"）。
//
// 兼容常见复制来源：
//   - DevTools Application → Cookies 复制的 URL 编码值或 URL 解码值（含末尾 Base64 `=` 填充与 `/`、`+`）
//   - DevTools Application → Cookies 整行复制（Tab 分隔列）
//   - Network 面板复制的 `Cookie: ...` / `cookie: ...` / `Set-Cookie: ...` 或多行原始请求头
//   - Copy as cURL（bash / Windows cmd `^` 转义）/ Copy as fetch / PowerShell
//   - Cookie-Editor 等扩展导出的 JSON 数组或凭证 JSON 对象
func NormalizeCookieInput(raw string) (header string, realmHint string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ""
	}
	realmHint = detectRealmHint(trimmed)

	if h, rh, ok := tryParseCookieJSON(trimmed); ok {
		if rh != "" {
			realmHint = rh
		}
		return h, realmHint
	}

	if extracted, ok := tryExtractFromCommand(trimmed); ok {
		return parseCookieString(extracted), realmHint
	}

	if extracted, ok := tryExtractHeaderLines(trimmed); ok {
		return extracted, realmHint
	}

	if extracted, ok := tryExtractTabRows(trimmed); ok {
		return extracted, realmHint
	}

	return parseCookieString(trimmed), realmHint
}

func detectRealmHint(s string) string {
	lower := strings.ToLower(s)
	hasCN := strings.Contains(lower, "todayai.cn") || strings.Contains(lower, "mytoday.cn")
	hasGlobal := strings.Contains(lower, "today.ai")
	switch {
	case hasCN && !hasGlobal:
		return "cn"
	case hasGlobal && !hasCN:
		return "global"
	default:
		return ""
	}
}

func tryParseCookieJSON(trimmed string) (string, string, bool) {
	if strings.HasPrefix(trimmed, "[") {
		var items []map[string]any
		if err := json.Unmarshal([]byte(trimmed), &items); err == nil {
			var pairs []string
			realm := ""
			for _, item := range items {
				if domain, ok := item["domain"].(string); ok && realm == "" {
					realm = detectRealmHint(domain)
				}
				name, _ := item["name"].(string)
				val, _ := item["value"].(string)
				name = strings.TrimSpace(name)
				val = strings.TrimSpace(val)
				if name != "" && val != "" && isCookieName(name) {
					pairs = append(pairs, formatCookiePair(name, val))
				}
			}
			return strings.Join(pairs, "; "), realm, true
		}
	}
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			realm := ""
			if r, ok := obj["realm"].(string); ok {
				switch strings.ToLower(strings.TrimSpace(r)) {
				case "cn":
					realm = "cn"
				case "global":
					realm = "global"
				}
			}
			if realm == "" {
				if domain, ok := obj["domain"].(string); ok {
					realm = detectRealmHint(domain)
				}
			}
			if cr, ok := obj["cookie_raw"].(string); ok && strings.TrimSpace(cr) != "" {
				h, rh := NormalizeCookieInput(cr)
				if realm == "" {
					realm = rh
				}
				return h, realm, true
			}
			if sess, ok := obj["session"].(string); ok && strings.TrimSpace(sess) != "" {
				h, rh := NormalizeCookieInput(sess)
				if realm == "" {
					realm = rh
				}
				return h, realm, true
			}
			if name, ok := obj["name"].(string); ok {
				if val, okVal := obj["value"].(string); okVal && strings.TrimSpace(name) != "" && strings.TrimSpace(val) != "" {
					return formatCookiePair(strings.TrimSpace(name), strings.TrimSpace(val)), realm, true
				}
			}
			// 兼容误把 /api/auth/get-session JSON 粘贴进来的情形，保留给后续诊断给出明确提示。
			if sessObj, ok := obj["session"].(map[string]any); ok {
				if tok, okTok := sessObj["token"].(string); okTok && strings.TrimSpace(tok) != "" {
					return formatCookiePair(SessionCookieName, strings.TrimSpace(tok)), realm, true
				}
			}
		}
	}
	return "", "", false
}

func tryExtractFromCommand(s string) (string, bool) {
	work := s
	lower := strings.ToLower(work)
	if strings.HasPrefix(lower, "curl") && strings.Contains(work, "^") {
		work = unescapeCmdCarets(work)
	}
	work = strings.ReplaceAll(work, "\\\r\n", " ")
	work = strings.ReplaceAll(work, "\\\n", " ")

	var parts []string
	for _, m := range reCurlCookieFlag.FindAllStringSubmatch(work, -1) {
		val := firstNonEmpty(m[1], unescapeQuotes(m[2]), m[3])
		if val != "" {
			parts = append(parts, val)
		}
	}
	for _, m := range reCurlHeaderFlag.FindAllStringSubmatch(work, -1) {
		val := firstNonEmpty(m[1], unescapeQuotes(m[2]))
		if val != "" {
			parts = append(parts, val)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "; "), true
	}

	if m := reFetchCookieKV.FindStringSubmatch(work); len(m) > 0 {
		val := firstNonEmpty(unescapeQuotes(m[1]), unescapeQuotes(m[2]))
		if val != "" {
			return val, true
		}
	}

	if matches := rePSCookie.FindAllStringSubmatch(work, -1); len(matches) > 0 {
		for _, m := range matches {
			if strings.TrimSpace(m[1]) != "" && strings.TrimSpace(m[2]) != "" {
				parts = append(parts, strings.TrimSpace(m[1])+"="+strings.TrimSpace(m[2]))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; "), true
		}
	}
	return "", false
}

func unescapeCmdCarets(s string) string {
	s = strings.ReplaceAll(s, "^\r\n", " ")
	s = strings.ReplaceAll(s, "^\n", " ")
	var buf strings.Builder
	buf.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '^' && i+1 < len(s) {
			i++
			buf.WriteByte(s[i])
			continue
		}
		buf.WriteByte(s[i])
	}
	return buf.String()
}

func unescapeQuotes(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, `\"`, `"`)
	s = strings.ReplaceAll(s, `\'`, `'`)
	s = strings.ReplaceAll(s, `\\`, `\`)
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func tryExtractHeaderLines(s string) (string, bool) {
	if !strings.Contains(s, "\n") {
		return "", false
	}
	var collected []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" {
			continue
		}
		if rest, ok := stripCookieHeaderPrefix(line); ok && rest != "" {
			parsed := parseCookieString(rest)
			if parsed != "" {
				collected = append(collected, parsed)
			}
		}
	}
	if len(collected) == 0 {
		return "", false
	}
	return strings.Join(collected, "; "), true
}

func tryExtractTabRows(s string) (string, bool) {
	if !strings.Contains(s, "\t") {
		return "", false
	}
	var pairs []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || !strings.Contains(line, "\t") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 2 {
			continue
		}
		name := strings.TrimSpace(cols[0])
		val := strings.TrimSpace(cols[1])
		if name == "" || val == "" || strings.EqualFold(name, "name") {
			continue
		}
		if isCookieName(name) {
			pairs = append(pairs, formatCookiePair(name, val))
		}
	}
	if len(pairs) == 0 {
		return "", false
	}
	return strings.Join(pairs, "; "), true
}

func stripCookieHeaderPrefix(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "cookie:"):
		return strings.TrimSpace(trimmed[len("cookie:"):]), true
	case strings.HasPrefix(lower, "set-cookie:"):
		return strings.TrimSpace(trimmed[len("set-cookie:"):]), true
	default:
		return trimmed, false
	}
}

func parseCookieString(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "\r", "")
	for {
		rest, stripped := stripCookieHeaderPrefix(s)
		if !stripped {
			break
		}
		s = rest
	}
	s = unquoteOuter(strings.TrimSpace(s))
	if s == "" {
		return ""
	}

	if strings.Contains(s, ";") {
		s = strings.ReplaceAll(s, "\n", " ")
		var pairs []string
		for _, seg := range strings.Split(s, ";") {
			seg = strings.TrimSpace(seg)
			if seg == "" || isCookieAttribute(seg) {
				continue
			}
			eq := strings.IndexByte(seg, '=')
			if eq < 0 {
				if len(pairs) == 0 {
					pairs = append(pairs, formatCookiePair(SessionCookieName, removeAllWhitespace(seg)))
				}
				continue
			}
			lhs := strings.TrimSpace(seg[:eq])
			rhs := strings.TrimSpace(seg[eq+1:])
			// 裸的 Better Auth 签名值末尾固定带 Base64 `=`，例如 `<token>.<43chars>=; HttpOnly; Secure`。
			if strings.Trim(rhs, "=") == "" || !isCookieName(lhs) {
				if lhs != "" && !isCookieAttributeKey(lhs) {
					pairs = append(pairs, formatCookiePair(SessionCookieName, removeAllWhitespace(seg)))
				}
				continue
			}
			if rhs != "" {
				pairs = append(pairs, formatCookiePair(lhs, rhs))
			}
		}
		return strings.Join(pairs, "; ")
	}

	// 单段输入：区分 `name=value` 与裸值（Better Auth 的 URL 解码签名值以 Base64 `=` 结尾，且可能含 `/`、`+`）。
	eq := strings.IndexByte(s, '=')
	if eq < 0 {
		clean := removeAllWhitespace(unquoteOuter(s))
		if clean == "" {
			return ""
		}
		return formatCookiePair(SessionCookieName, clean)
	}
	lhs := strings.TrimSpace(s[:eq])
	rhs := strings.TrimSpace(s[eq+1:])
	if strings.Trim(rhs, "=") == "" || !isCookieName(lhs) {
		clean := removeAllWhitespace(unquoteOuter(s))
		if clean == "" {
			return ""
		}
		return formatCookiePair(SessionCookieName, clean)
	}
	return formatCookiePair(lhs, removeAllWhitespace(unquoteOuter(rhs)))
}

func formatCookiePair(name, value string) string {
	name = strings.TrimSpace(name)
	value = unquoteOuter(strings.TrimSpace(value))
	if isBetterAuthCookie(name) {
		value = encodeBetterAuthValue(value)
	}
	return name + "=" + value
}

func isBetterAuthCookie(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "better-auth") || strings.Contains(lower, "session_token") || strings.Contains(lower, "session_data")
}

// encodeBetterAuthValue 先还原已有的百分号转义（避免重复编码 `%` → `%25`），再按
// JS encodeURIComponent 规则编码，确保 `+` `/` `=` 在线格式与浏览器一致。
func encodeBetterAuthValue(val string) string {
	if strings.Contains(val, "%") {
		// 必须用 PathUnescape 而非 QueryUnescape：后者会把 Base64 里的 `+` 破坏成空格。
		if decoded, err := url.PathUnescape(val); err == nil {
			val = decoded
		}
	}
	return encodeURIComponent(val)
}

const hexUpper = "0123456789ABCDEF"

// encodeURIComponent 与 ECMAScript encodeURIComponent 保持一致：
// 保留 A-Z a-z 0-9 - _ . ! ~ * ' ( )，其余字节按大写 %XX 编码。
func encodeURIComponent(s string) string {
	var buf strings.Builder
	buf.Grow(len(s) + 16)
	for i := 0; i < len(s); i++ {
		b := s[i]
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == '!' || b == '~' || b == '*' || b == '\'' || b == '(' || b == ')' {
			buf.WriteByte(b)
			continue
		}
		buf.WriteByte('%')
		buf.WriteByte(hexUpper[b>>4])
		buf.WriteByte(hexUpper[b&0x0F])
	}
	return buf.String()
}

func unquoteOuter(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func removeAllWhitespace(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// isCookieName 按 RFC 7230 §3.2.6 tchar 校验 Cookie 名（不含 `/`、空白、引号、分号等）。
func isCookieName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case b >= '0' && b <= '9',
			b >= 'A' && b <= 'Z',
			b >= 'a' && b <= 'z',
			b == '!', b == '#', b == '$', b == '%', b == '&', b == '\'',
			b == '*', b == '+', b == '-', b == '.', b == '^', b == '_',
			b == '`', b == '|', b == '~':
		default:
			return false
		}
	}
	return true
}

func isCookieAttribute(seg string) bool {
	key := seg
	if eq := strings.IndexByte(seg, '='); eq >= 0 {
		key = seg[:eq]
	}
	return isCookieAttributeKey(strings.TrimSpace(key))
}

func isCookieAttributeKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "path", "domain", "max-age", "expires", "samesite", "httponly", "secure", "partitioned", "priority":
		return true
	}
	return false
}

// CookiesFromHeaders 从上游响应头提取有效的 Better Auth 会话 Cookie（大小写无关，跳过清除项）。
func CookiesFromHeaders(headers http.Header) string {
	if len(headers) == 0 {
		return ""
	}
	var rawSetCookies []string
	for k, vals := range headers {
		if strings.EqualFold(k, "Set-Cookie") {
			rawSetCookies = append(rawSetCookies, vals...)
		}
	}
	var parts []string
	seen := map[string]int{}
	for _, raw := range rawSetCookies {
		for _, single := range splitSetCookie(raw) {
			segments := strings.Split(single, ";")
			if len(segments) == 0 {
				continue
			}
			nv := strings.TrimSpace(segments[0])
			eq := strings.IndexByte(nv, '=')
			if eq <= 0 {
				continue
			}
			name := strings.TrimSpace(nv[:eq])
			val := strings.TrimSpace(nv[eq+1:])
			if val == "" || isClearedSetCookie(segments[1:]) {
				continue
			}
			if !isBetterAuthCookie(name) {
				continue
			}
			formatted := formatCookiePair(name, val)
			if idx, exists := seen[name]; exists {
				parts[idx] = formatted
			} else {
				seen[name] = len(parts)
				parts = append(parts, formatted)
			}
		}
	}
	return strings.Join(parts, "; ")
}

func isClearedSetCookie(attrs []string) bool {
	for _, a := range attrs {
		a = strings.TrimSpace(a)
		lower := strings.ToLower(a)
		if strings.HasPrefix(lower, "max-age=") {
			v := strings.TrimSpace(lower[len("max-age="):])
			if v == "0" || strings.HasPrefix(v, "-") {
				return true
			}
		}
	}
	return false
}

// splitSetCookie 拆分可能被代理合并为单行的逗号分隔 Set-Cookie（避开 Expires=Thu, ... 中的逗号）。
func splitSetCookie(header string) []string {
	var out []string
	start := 0
	for i := 0; i < len(header); i++ {
		if header[i] != ',' {
			continue
		}
		j := i + 1
		for j < len(header) && header[j] == ' ' {
			j++
		}
		k := j
		for k < len(header) && header[k] != '=' && header[k] != ';' && header[k] != ',' {
			k++
		}
		if k < len(header) && header[k] == '=' && isCookieName(strings.TrimSpace(header[j:k])) {
			if part := strings.TrimSpace(header[start:i]); part != "" {
				out = append(out, part)
			}
			start = j
			i = j - 1
		}
	}
	if last := strings.TrimSpace(header[start:]); last != "" {
		out = append(out, last)
	}
	return out
}

// DiagnoseCookieFailure 针对 401/403 拒绝结果给出具体的中文排障提示。
func DiagnoseCookieFailure(cookieHeader string) string {
	var sessionValues []string
	for _, seg := range strings.Split(cookieHeader, ";") {
		seg = strings.TrimSpace(seg)
		eq := strings.IndexByte(seg, '=')
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(seg[:eq])
		val := strings.TrimSpace(seg[eq+1:])
		if strings.Contains(strings.ToLower(name), "session_token") && val != "" {
			if decoded, err := url.PathUnescape(val); err == nil {
				val = decoded
			}
			sessionValues = append(sessionValues, val)
		}
	}
	if len(sessionValues) == 0 {
		return "未在粘贴内容中找到 __Secure-better-auth.session_token（请先在浏览器登录 today.ai 或 todayai.cn，再从 F12 → Application → Cookies 中复制该项）"
	}
	for _, v := range sessionValues {
		if strings.Contains(v, ".") {
			return "Cookie 已被上游拒绝（会话可能已过期或在浏览器中退出登录，请重新登录后复制最新的 __Secure-better-auth.session_token）"
		}
	}
	return "session_token 缺少签名段（完整值形如 xxx.yyy=），请从浏览器 F12 → Application → Cookies 中复制完整的 __Secure-better-auth.session_token 值"
}
