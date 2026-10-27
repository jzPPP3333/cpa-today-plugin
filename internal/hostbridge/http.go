package hostbridge

// 经宿主 HTTP 桥出站。所有上游请求都走这里，
// 这样全局代理 / 账号级代理 / 出站日志 / 用量统计才生效。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"cpa-today-plugin/cpasdk/pluginabi"
)

// httpResponse 同时接受 Go 字段名与 snake_case 两种序列化形态。
type httpResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
	statusSeen bool
}

func (r *httpResponse) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(data, &fields); errUnmarshal != nil {
		return errUnmarshal
	}
	for key, value := range fields {
		switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
		case "statuscode":
			r.statusSeen = true
			if errDecode := json.Unmarshal(value, &r.StatusCode); errDecode != nil {
				return fmt.Errorf("decode status: %w", errDecode)
			}
		case "headers":
			_ = json.Unmarshal(value, &r.Headers)
		case "body":
			_ = json.Unmarshal(value, &r.Body)
		}
	}
	return nil
}

// Response 是一次完整的上游响应。
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// Do 发起一次非流式上游请求（经宿主 host.http.do）。
func Do(ctx context.Context, req *http.Request) (*Response, error) {
	body, errBody := readAndClose(req.Body)
	if errBody != nil {
		return nil, errBody
	}
	payload := map[string]any{
		"method":  req.Method,
		"url":     req.URL.String(),
		"headers": req.Header,
		"body":    body,
	}
	// 显式透传代理：宿主的 host.http.do 在某些版本里不会自动套用
	// requests.proxy-url，插件侧指定才可靠（实测直连 11s / 走代理 1s）。
	if proxy := ProxyURL(); proxy != "" {
		payload["proxy_url"] = proxy
	}
	raw, errCall := CallCtx(ctx, pluginabi.MethodHostHTTPDo, payload)
	if errCall != nil {
		return nil, fmt.Errorf("host.http.do: %w", errCall)
	}
	var rpc httpResponse
	if errUnmarshal := json.Unmarshal(raw, &rpc); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host http response: %w", errUnmarshal)
	}
	if !rpc.statusSeen {
		return nil, fmt.Errorf("host http response has no status field (host ABI mismatch): %.200s", raw)
	}
	return &Response{StatusCode: rpc.StatusCode, Headers: rpc.Headers, Body: rpc.Body}, nil
}

// DoJSON 以 JSON 方式发起请求并返回原始响应。
func DoJSON(ctx context.Context, method, url string, headers map[string]string, body any) (*Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			return nil, errMarshal
		}
		reader = bytes.NewReader(encoded)
	}
	req, errNew := http.NewRequestWithContext(ctx, method, url, reader)
	if errNew != nil {
		return nil, errNew
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return Do(ctx, req)
}

func readAndClose(body io.ReadCloser) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	raw, errRead := io.ReadAll(body)
	if errClose := body.Close(); errClose != nil && errRead == nil {
		return nil, fmt.Errorf("close request body: %w", errClose)
	}
	if errRead != nil {
		return nil, fmt.Errorf("read request body: %w", errRead)
	}
	return raw, nil
}

// DefaultTimeout 是上游短请求（换 token、列模型、查额度）的超时。
const DefaultTimeout = 45 * time.Second

// AuthSave 把凭证 JSON 写回宿主的磁盘凭证文件。
func AuthSave(ctx context.Context, name string, payload []byte) error {
	_, errCall := CallCtx(ctx, pluginabi.MethodHostAuthSave, map[string]any{
		"name": name,
		"json": json.RawMessage(payload),
	})
	return errCall
}

// AuthList 列出宿主登记的全部凭证（宿主返回原始 JSON，交由调用方筛选）。
func AuthList(ctx context.Context) (json.RawMessage, error) {
	return CallCtx(ctx, pluginabi.MethodHostAuthList, map[string]any{})
}

// HostAuthListEntry 是 host.auth.list 返回的一条记录。
type HostAuthListEntry struct {
	ID        string `json:"id"`
	AuthIndex string `json:"auth_index"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Label     string `json:"label"`
	Status    string `json:"status"`
	Disabled  bool   `json:"disabled"`
	Path      string `json:"path,omitempty"`
}

// ListAuths 解析 host.auth.list 的两种返回形态。
func ListAuths(ctx context.Context) ([]HostAuthListEntry, error) {
	raw, errCall := AuthList(ctx)
	if errCall != nil {
		return nil, errCall
	}
	var entries []HostAuthListEntry
	if errUnmarshal := json.Unmarshal(raw, &entries); errUnmarshal == nil {
		return entries, nil
	}
	var wrapper struct {
		Auths []HostAuthListEntry `json:"auths"`
		Files []HostAuthListEntry `json:"files"`
	}
	if errUnmarshal := json.Unmarshal(raw, &wrapper); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.auth.list: %w", errUnmarshal)
	}
	merged := append([]HostAuthListEntry{}, wrapper.Auths...)
	seen := map[string]bool{}
	for _, a := range merged {
		seen[a.Name] = true
		seen[a.ID] = true
	}
	for _, f := range wrapper.Files {
		if !seen[f.Name] && !seen[f.ID] {
			merged = append(merged, f)
		}
	}
	return merged, nil
}

// Log 向宿主日志系统输出一行（可选，失败静默）。
func Log(level, message string) {
	_, _ = Call("", pluginabi.MethodHostLog, map[string]any{"level": level, "message": message})
}

var _ = errors.New

// proxyOverride 是插件级的出站代理，由 app 层在配置变更时注入。
var (
	proxyMu       sync.RWMutex
	proxyOverride string
)

// SetProxyURL 设置出站代理（空串表示交给宿主决定）。
func SetProxyURL(raw string) {
	proxyMu.Lock()
	proxyOverride = strings.TrimSpace(raw)
	proxyMu.Unlock()
}

// ProxyURL 返回当前生效的插件代理。
func ProxyURL() string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	return proxyOverride
}
