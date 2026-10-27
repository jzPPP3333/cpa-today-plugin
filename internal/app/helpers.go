package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"cpa-today-plugin/internal/hostbridge"
	"cpa-today-plugin/internal/today"
)

// firstCredential 取出本插件的第一个可用凭证（控制台直连测试用）。
func (a *App) firstCredential() (today.Credential, error) {
	entries, errList := hostbridge.ListAuths(context.Background())
	if errList != nil {
		return today.Credential{}, errList
	}
	for _, e := range entries {
		if e.Disabled {
			continue
		}
		if !strings.EqualFold(e.Provider, ProviderKey) && !strings.HasPrefix(strings.ToLower(e.Name), "today-") {
			continue
		}
		payload, errGet := hostbridge.Call("", "host.auth.get", map[string]any{"auth_index": e.AuthIndex})
		if errGet != nil {
			continue
		}
		var wrapper struct {
			JSON json.RawMessage `json:"json"`
		}
		if errUnmarshal := json.Unmarshal(payload, &wrapper); errUnmarshal != nil || len(wrapper.JSON) == 0 {
			continue
		}
		if cred, ok := parseCredential(wrapper.JSON, e.Name); ok {
			return cred, nil
		}
	}
	return today.Credential{}, errNoCredential
}

type noCredentialError struct{}

func (noCredentialError) Error() string { return "no today credential available" }

var errNoCredential = noCredentialError{}

// clientPool 按凭证 UID 复用 Client，让 JWT 与模型选择缓存跨请求生效。
//
// 每次执行都新建 Client 会让两个最贵的往返（换 token、查 pricing）
// 在每个请求上重做一遍——实测各 1.4s 左右，直接体现在首字延迟上。
var clientPool = struct {
	sync.Mutex
	items map[string]*today.Client
}{items: map[string]*today.Client{}}

// clientFor 返回该凭证的长生命周期客户端。
func clientFor(cred today.Credential, cfg Config) *today.Client {
	key := cred.UID + "|" + cfg.AppBase + "|" + cfg.APIBase
	clientPool.Lock()
	defer clientPool.Unlock()
	if c, ok := clientPool.items[key]; ok {
		return c
	}
	c := today.NewClientWithBase(cred, cfg.AppBase, cfg.APIBase)
	clientPool.items[key] = c
	return c
}
