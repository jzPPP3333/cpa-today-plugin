package app

// 插件组合根与 ABI 适配层。
//
// 能力：
//   - auth provider：解析 today-*.json 凭证、登录（粘贴 cookie / 邮箱 OTP）
//   - model provider：静态模型 + 按凭证探测
//   - executor：把 OpenAI chat-completions 请求翻译成 today 私有协议并轮询取回
//   - quota provider：读 /v1/pricing/status 的套餐与用量
//   - management：/plugins/today/status 与 /plugins/today/console 控制台

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"cpa-today-plugin/cpasdk/pluginabi"
	"cpa-today-plugin/cpasdk/pluginapi"
	"cpa-today-plugin/internal/core"
	"cpa-today-plugin/internal/hostbridge"
	"cpa-today-plugin/internal/today"
)

const (
	PluginID       = "today"
	DisplayName    = "Today"
	ProviderKey    = "today"
	PluginAuthor   = "cpa-today-plugin"
	PluginRepo     = "https://github.com/local/cpa-today-plugin"
	DefaultVersion = "0.1.0"

	FormatChatCompletions = "chat-completions"
)

// App 是插件运行实例。
type App struct {
	version string

	mu  sync.Mutex
	cfg Config
	// probedModels 保存最近一次从上游探测到的模型 ID（不含前缀）。
	probedModels []string
	streams      sync.WaitGroup
	cancels      map[uint64]func()
	nextID       uint64
}

// New 构造 App。
func New(version string) *App {
	if version == "" {
		version = DefaultVersion
	}
	return &App{version: version, cfg: DefaultConfig(), cancels: map[uint64]func(){}}
}

// HandleMethod 是插件 ABI 的总入口。
func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return a.handleLifecycle(method, request)

	case pluginabi.MethodPluginQuiesce:
		a.StopStreams()
		return core.OKEnvelope(struct{}{})

	case pluginabi.MethodAuthIdentifier:
		return core.OKEnvelope(identifierResponse{Identifier: ProviderKey})
	case pluginabi.MethodAuthParse:
		return a.handleAuthParse(request)
	case pluginabi.MethodAuthLoginStart:
		return a.handleAuthLoginStart(request)
	case pluginabi.MethodAuthLoginPoll:
		return a.handleAuthLoginPoll(request)
	case pluginabi.MethodAuthRefresh:
		return a.handleAuthRefresh(request)

	case pluginabi.MethodModelRegister, pluginabi.MethodModelStatic:
		return a.handleModelStatic(request)
	case pluginabi.MethodModelForAuth:
		return a.handleModelForAuth(request)

	case pluginabi.MethodExecutorIdentifier:
		return core.OKEnvelope(identifierResponse{Identifier: ProviderKey})
	case pluginabi.MethodExecutorExecute:
		return a.handleExecute(request, false)
	case pluginabi.MethodExecutorExecuteStream:
		return a.handleExecute(request, true)
	case pluginabi.MethodExecutorCountTokens:
		return a.handleCountTokens(request)

	case pluginabi.MethodQuotaIdentifier:
		return core.OKEnvelope(identifierResponse{Identifier: ProviderKey})
	case pluginabi.MethodQuotaDescribe:
		return a.handleQuotaDescribe()
	case pluginabi.MethodQuotaFetch:
		return a.handleQuotaFetch(request)
	case pluginabi.MethodQuotaReset:
		return core.OKEnvelope(map[string]any{"ok": true, "message": "today has no resettable quota"})

	case pluginabi.MethodManagementRegister:
		return core.OKEnvelope(a.managementRegistration())
	case pluginabi.MethodManagementHandle:
		return a.handleManagement(request)
	}
	return core.ErrorEnvelope("unknown_method", "unknown method: "+method), nil
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32 `json:"schema_version"`
	// Metadata 必须用 SDK 结构体：宿主侧 pluginapi.Metadata 没有 json tag，
	// 按 Go 字段名（Name/Version/Author/GitHubRepository）解码。
	Metadata     pluginapi.Metadata       `json:"metadata"`
	Capabilities registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ModelProvider         bool     `json:"model_provider"`
	AuthProvider          bool     `json:"auth_provider"`
	Executor              bool     `json:"executor"`
	QuotaProvider         bool     `json:"quota_provider"`
	ManagementAPI         bool     `json:"management_api"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
}

func (a *App) handleLifecycle(method string, request []byte) ([]byte, error) {
	var req lifecycleRequest
	if len(request) > 0 {
		_ = json.Unmarshal(request, &req)
	}
	cfg, errCfg := DecodeConfig(req.ConfigYAML)
	if errCfg != nil {
		return nil, fmt.Errorf("%s: %w", method, errCfg)
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	hostbridge.SetProxyURL(cfg.ProxyURL)
	return core.OKEnvelope(a.Registration())
}

// Registration 返回注册响应。
func (a *App) Registration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             DisplayName,
			Version:          a.version,
			Author:           PluginAuthor,
			GitHubRepository: PluginRepo,
		},
		Capabilities: registrationCapabilities{
			ModelProvider:         true,
			AuthProvider:          true,
			Executor:              true,
			QuotaProvider:         true,
			ManagementAPI:         true,
			ExecutorModelScope:    "both",
			ExecutorInputFormats:  []string{FormatChatCompletions},
			ExecutorOutputFormats: []string{FormatChatCompletions},
		},
	}
}

// Config 返回当前配置。
func (a *App) Config() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// BeginStream 注册一个流式任务。
func (a *App) BeginStream() (context.Context, context.CancelFunc, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.nextID++
	id := a.nextID
	key := id
	a.cancels[key] = cancel
	a.mu.Unlock()
	a.streams.Add(1)
	var once sync.Once
	finish := func() {
		once.Do(func() {
			a.mu.Lock()
			delete(a.cancels, key)
			a.mu.Unlock()
			cancel()
			a.streams.Done()
		})
	}
	return ctx, cancel, finish
}

// StopStreams 取消并等待在途流。
func (a *App) StopStreams() {
	a.mu.Lock()
	cancels := make([]func(), 0, len(a.cancels))
	for _, c := range a.cancels {
		cancels = append(cancels, c)
	}
	a.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	a.streams.Wait()
}

func decodeRequest(raw []byte, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	return nil
}

func isChatFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "openai", "chat-completions", "chat_completions", "openai-chat-completions":
		return true
	}
	return false
}

var _ = http.StatusOK

// setProbedModels 记录一次上游探测到的模型清单（已去掉 today- 前缀）。
func (a *App) setProbedModels(ids []string) {
	a.mu.Lock()
	a.probedModels = ids
	a.mu.Unlock()
}

// ProbedModels 返回最近一次探测到的模型 ID；为空时回退到已知表。
func (a *App) ProbedModels() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.probedModels) > 0 {
		return append([]string(nil), a.probedModels...)
	}
	out := make([]string, 0, len(today.KnownModels))
	for _, m := range today.KnownModels {
		out = append(out, m.ID)
	}
	return out
}
