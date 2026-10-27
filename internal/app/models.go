package app

// model provider：静态模型清单与按凭证探测。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"cpa-today-plugin/cpasdk/pluginapi"
	"cpa-today-plugin/internal/core"
	"cpa-today-plugin/internal/hostbridge"
	"cpa-today-plugin/internal/today"
)

type staticModelRPC struct {
	pluginapi.StaticModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type authModelRPC struct {
	pluginapi.AuthModelRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

// ModelPrefix 是插件模型对外暴露的前缀。
//
// 必须带前缀：today 的模型短名（deepseek-v4.1-flash / glm-5.3）与内置
// openai-compatibility 上游的别名重名，宿主合并模型列表时同名条目会互相
// 覆盖，请求可能落到另一家供应商。带前缀后两者共存且可精确指定。
//
// 用连字符而不是斜杠：斜杠在宿主里是 provider 前缀分隔符（prefix/model）。
const ModelPrefix = "today-"

// PublicModelID 返回客户端可见的模型名。
func PublicModelID(id string) string { return ModelPrefix + id }

// StripModelPrefix 去掉客户端传来的插件前缀。
func StripModelPrefix(name string) string {
	trimmed := strings.TrimSpace(name)
	if strings.HasPrefix(strings.ToLower(trimmed), ModelPrefix) {
		return trimmed[len(ModelPrefix):]
	}
	return trimmed
}

func modelInfo(m today.Model) pluginapi.ModelInfo {
	return pluginapi.ModelInfo{
		ID:          PublicModelID(m.ID),
		Object:      "model",
		OwnedBy:     ProviderKey,
		Type:        "chat",
		DisplayName: m.Title,
		Name:        m.OptionID,
		Description: "Today (" + m.Title + ")",
		// 上游未公开上下文上限；按 128k 保守声明，避免客户端塞超长上下文。
		ContextLength:             131072,
		InputTokenLimit:           131072,
		OutputTokenLimit:          8192,
		MaxCompletionTokens:       8192,
		SupportedParameters:       []string{"messages", "stream", "temperature"},
		SupportedInputModalities:  []string{"text"},
		SupportedOutputModalities: []string{"text"},
	}
}

func staticModels() []pluginapi.ModelInfo {
	out := make([]pluginapi.ModelInfo, 0, len(today.KnownModels))
	for _, m := range today.KnownModels {
		out = append(out, modelInfo(m))
	}
	return out
}

func (a *App) handleModelStatic(request []byte) ([]byte, error) {
	var rpc staticModelRPC
	_ = decodeRequest(request, &rpc)
	return core.OKEnvelope(pluginapi.ModelResponse{Provider: ProviderKey, Models: staticModels()})
}

// handleModelForAuth 用该凭证探测上游可用模型。
//
// 探测失败时不返回空清单（宿主会据此淘汰凭证），而是回退到静态清单：
// 一次瞬时网络抖动不该让账号从模型列表里消失。
func (a *App) handleModelForAuth(request []byte) ([]byte, error) {
	var rpc authModelRPC
	if errDecode := decodeRequest(request, &rpc); errDecode != nil {
		return core.OKEnvelope(pluginapi.ModelResponse{Provider: ProviderKey, Models: staticModels()})
	}
	cred, ok := parseCredential(rpc.StorageJSON, rpc.AuthID+".json")
	if !ok {
		return core.OKEnvelope(pluginapi.ModelResponse{Provider: ProviderKey})
	}
	ctx := hostbridge.WithCallbackID(context.Background(), rpc.HostCallbackID)
	cfg := a.Config()
	client := clientFor(cred, cfg)
	status, errStatus := client.PricingStatus(ctx)
	if errStatus != nil {
		hostbridge.Log("warn", "today: model probe failed for "+cred.UID+": "+errStatus.Error())
		return core.OKEnvelope(pluginapi.ModelResponse{Provider: ProviderKey, Models: staticModels()})
	}
	models := modelsFromPricing(status)
	if len(models) == 0 {
		models = staticModels()
	}
	// ModelInfo.ID 已带 today- 前缀，记录时剥掉，避免状态页拼出双前缀。
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, StripModelPrefix(m.ID))
	}
	a.setProbedModels(ids)
	return core.OKEnvelope(pluginapi.ModelResponse{Provider: ProviderKey, Models: models})
}

// modelsFromPricing 从 /v1/pricing/status 的 modelOptions 里读出可用模型。
func modelsFromPricing(status map[string]any) []pluginapi.ModelInfo {
	options, _ := status["modelOptions"].(map[string]any)
	if options == nil {
		return nil
	}
	rawList, _ := options["options"].([]any)
	var out []pluginapi.ModelInfo
	seen := map[string]bool{}
	for _, raw := range rawList {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		optionID, _ := item["id"].(string)
		if optionID == "" {
			continue
		}
		disabled, _ := item["disabled"].(bool)
		if disabled {
			continue
		}
		short := strings.TrimPrefix(optionID, "model.")
		if seen[short] {
			continue
		}
		seen[short] = true
		display := short
		if displayBlock, okDisplay := item["display"].(map[string]any); okDisplay {
			if title, okTitle := displayBlock["title"].(string); okTitle && title != "" {
				display = title
			}
		}
		info := modelInfo(today.Model{OptionID: optionID, ID: short, Title: display})
		out = append(out, info)
	}
	return out
}

var _ = json.Marshal
var _ = http.StatusOK
