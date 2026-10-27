package app

// executor：把 OpenAI chat-completions 请求翻译成 today 私有协议。
//
// 上游没有 SSE，executor 只能：
//   1. 发送一轮消息（POST /v1/messages）；
//   2. 轮询 /v1/conversations/messages 直到 assistant 消息 state=completed；
//   3. 非流式：一次性返回完整 chat.completion；
//      流式：把增量正文按 OpenAI chunk 形状 emit 给宿主。

import (
	"bytes"
	"context"
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

type executorRPC struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type streamResponse struct {
	Headers http.Header `json:"headers,omitempty"`
}

// chatMessage 是 OpenAI chat 消息。
type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// chatRequest 只取需要的字段。
type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

func (a *App) handleExecute(request []byte, stream bool) ([]byte, error) {
	var rpc executorRPC
	if errDecode := decodeRequest(request, &rpc); errDecode != nil {
		return nil, core.NewPluginError("invalid_request", errDecode.Error(), http.StatusBadRequest)
	}
	if !isChatFormat(rpc.Format) || (rpc.SourceFormat != "" && !isChatFormat(rpc.SourceFormat)) {
		return nil, core.NewPluginError("unsupported_format",
			fmt.Sprintf("today plugin only handles %s (got format=%q source=%q)", FormatChatCompletions, rpc.Format, rpc.SourceFormat),
			http.StatusBadRequest)
	}

	cred, errCred := credentialFromRPC(rpc.ExecutorRequest)
	if errCred != nil {
		return nil, errCred
	}
	cfg := a.Config()

	body := rpc.Payload
	if len(body) == 0 {
		body = rpc.OriginalRequest
	}
	var chat chatRequest
	if errUnmarshal := json.Unmarshal(body, &chat); errUnmarshal != nil {
		return nil, core.NewPluginError("invalid_request", "request body is not chat-completions JSON: "+errUnmarshal.Error(), http.StatusBadRequest)
	}

	// 客户端名可能带 today- 前缀（本插件模型与内置上游同名，必须靠前缀区分）。
	modelName := StripModelPrefix(rpc.Model)
	if modelName == "" {
		modelName = StripModelPrefix(chat.Model)
	}
	if modelName == "" {
		modelName = StripModelPrefix(cfg.DefaultModel)
	}
	model, okModel := today.ModelByID(modelName)
	if !okModel {
		return nil, core.NewPluginError("model_not_found",
			fmt.Sprintf("today does not serve model %q", modelName), http.StatusNotFound)
	}

	prompt := buildPrompt(chat.Messages, cfg.ContextTurns)
	if strings.TrimSpace(prompt) == "" {
		return nil, core.NewPluginError("invalid_request", "prompt is empty", http.StatusBadRequest)
	}
	if cfg.ReasoningHint != "" {
		prompt += cfg.ReasoningHint
	}

	client := clientFor(cred, cfg)
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 240 * time.Second
	}

	if stream {
		// 凭证校验必须在返回流响应头之前完成：一旦提交 SSE 头，
		// 上游错误就只能是 500 帧，宿主无法据此冷却或轮转凭证。
		preCtx, preCancel := context.WithTimeout(
			hostbridge.WithCallbackID(context.Background(), rpc.HostCallbackID), 45*time.Second)
		_, errToken := client.Token(preCtx)
		preCancel()
		if errToken != nil {
			return nil, core.ToPluginError(errToken)
		}
		return a.startStream(rpc, client, model, prompt, timeout)
	}

	ctx, cancel := context.WithTimeout(hostbridge.WithCallbackID(context.Background(), rpc.HostCallbackID), timeout)
	defer cancel()

	t0 := time.Now()
	if errModel := ensureModel(ctx, client, model); errModel != nil {
		return nil, core.ToPluginError(errModel)
	}
	tModel := time.Since(t0)
	threadID, errSend := client.Send(ctx, prompt, "")
	if errSend != nil {
		return nil, core.ToPluginError(errSend)
	}
	tSend := time.Since(t0)
	reply, errReply := client.Reply(ctx, threadID, timeout, nil)
	if errReply != nil {
		return nil, core.ToPluginError(errReply)
	}
	hostbridge.Log("info", fmt.Sprintf(
		"today: timing model=%s send=%s total=%s prompt=%dB reply=%d",
		model.ID, tSend-tModel, time.Since(t0), len(prompt), len(reply)))

	return core.OKEnvelope(pluginapi.ExecutorResponse{
		Payload: completionPayload(PublicModelID(model.ID), reply),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}

func (a *App) startStream(rpc executorRPC, client *today.Client, model today.Model, prompt string, timeout time.Duration) ([]byte, error) {
	if strings.TrimSpace(rpc.StreamID) == "" {
		return nil, core.NewPluginError("invalid_request", "stream_id is required for streaming execution", http.StatusBadRequest)
	}
	callbackID := rpc.HostCallbackID
	streamID := rpc.StreamID

	ctx, cancel, finish := a.BeginStream()
	ctx = hostbridge.WithCallbackID(ctx, callbackID)
	go func() {
		defer finish()
		runStream(ctx, cancel, callbackID, streamID, client, model, prompt, timeout)
	}()

	return core.OKEnvelope(streamResponse{Headers: http.Header{
		"Content-Type":  []string{"text/event-stream"},
		"Cache-Control": []string{"no-cache"},
	}})
}

func runStream(ctx context.Context, cancel context.CancelFunc, callbackID, streamID string,
	client *today.Client, model today.Model, prompt string, timeout time.Duration) {

	workCtx, workCancel := context.WithTimeout(ctx, timeout)
	defer workCancel()

	emitText := func(text string) error {
		if text == "" {
			return nil
		}
		chunk := streamChunkPayload(PublicModelID(model.ID), text)
		return emitChunk(callbackID, streamID, chunk)
	}

	if errModel := ensureModel(workCtx, client, model); errModel != nil {
		closeStream(callbackID, streamID, errModel.Error())
		return
	}
	threadID, errSend := client.Send(workCtx, prompt, "")
	if errSend != nil {
		closeStream(callbackID, streamID, errSend.Error())
		return
	}
	_, errReply := client.Reply(workCtx, threadID, timeout, func(delta string) {
		if errEmit := emitText(delta); errEmit != nil {
			// 下游断开：取消上游轮询并静默收尾。
			cancel()
		}
	})
	if errReply != nil && workCtx.Err() == nil {
		closeStream(callbackID, streamID, errReply.Error())
		return
	}
	// 宿主需要一个带 finish_reason 的终帧才会正常收尾。
	_ = emitChunk(callbackID, streamID, finishChunkPayload(PublicModelID(model.ID)))
	closeStream(callbackID, streamID, "")
}

// ensureModel 确保上游当前选中的是请求的模型。
//
// 每次请求都打一次 pricing/status 会白加 1.4s（实测走代理的往返），
// 因此结果在客户端里缓存 10 分钟：模型切换在同一会话里极少发生。
func ensureModel(ctx context.Context, client *today.Client, model today.Model) error {
	client.LockModelCache()
	defer client.UnlockModelCache()
	if client.ModelOptionID() == model.OptionID {
		return nil
	}
	if client.ModelCheckedRecently() {
		return nil
	}
	selected, errSelected := client.SelectedModel(ctx)
	if errSelected != nil {
		// 读不到当前模型不算致命：继续发消息，让上游自己判定。
		return nil
	}
	client.MarkModelChecked(selected)
	if selected == model.OptionID {
		return nil
	}
	if errSet := client.SetModel(ctx, model.OptionID); errSet != nil {
		return errSet
	}
	client.MarkModelChecked(model.OptionID)
	return nil
}

// buildPrompt 把 chat messages 压成一个纯文本提示词。
//
// today 的 /v1/messages 只接受单轮文本，没有 system role，
// 因此历史轮次按「[用户]/[助手]」标签线性拼接。
func buildPrompt(messages []chatMessage, contextTurns int) string {
	if len(messages) == 0 {
		return ""
	}
	var system []string
	type turn struct{ role, text string }
	var turns []turn
	for _, m := range messages {
		text := contentToText(m.Content)
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			if text != "" {
				system = append(system, text)
			}
		case "assistant":
			turns = append(turns, turn{role: "助手", text: text})
		default:
			turns = append(turns, turn{role: "用户", text: text})
		}
	}
	if len(turns) == 0 {
		return strings.Join(system, "\n\n")
	}
	// 只保留最后 contextTurns 轮历史 + 当前消息。
	if contextTurns <= 0 {
		turns = turns[len(turns)-1:]
	} else if len(turns) > contextTurns+1 {
		turns = turns[len(turns)-(contextTurns+1):]
	}
	var buf strings.Builder
	if len(system) > 0 {
		buf.WriteString("[系统指令]\n")
		buf.WriteString(strings.Join(system, "\n\n"))
		buf.WriteString("\n\n")
	}
	if len(turns) > 1 {
		buf.WriteString("[历史对话]\n")
		for _, t := range turns[:len(turns)-1] {
			buf.WriteString(t.role)
			buf.WriteString("：")
			buf.WriteString(t.text)
			buf.WriteString("\n")
		}
		buf.WriteString("[当前消息]\n")
	}
	buf.WriteString(turns[len(turns)-1].text)
	return strings.TrimSpace(buf.String())
}

func contentToText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			block, okBlock := item.(map[string]any)
			if !okBlock {
				continue
			}
			blockType, _ := block["type"].(string)
			if blockType != "" && blockType != "text" && blockType != "input_text" && blockType != "output_text" {
				continue
			}
			if text, okText := block["text"].(string); okText {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text, okText := v["text"].(string); okText {
			return text
		}
	}
	encoded, errMarshal := json.Marshal(content)
	if errMarshal != nil {
		return ""
	}
	return string(encoded)
}

func completionPayload(modelID, text string) []byte {
	resp := map[string]any{
		"id":      "chatcmpl-" + today.RandomID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     0,
			"completion_tokens": estimateTokens(text),
			"total_tokens":      estimateTokens(text),
		},
	}
	encoded, _ := json.Marshal(resp)
	return encoded
}

func streamChunkPayload(modelID, delta string) []byte {
	chunk := map[string]any{
		"id":      "chatcmpl-" + today.RandomID(),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{"content": delta},
			"finish_reason": nil,
		}},
	}
	encoded, _ := json.Marshal(chunk)
	return encoded
}

// finishChunkPayload 是流终止帧：finish_reason=stop，delta 为空。
func finishChunkPayload(modelID string) []byte {
	chunk := map[string]any{
		"id":      "chatcmpl-" + today.RandomID(),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
	}
	encoded, _ := json.Marshal(chunk)
	return encoded
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return len([]rune(text))/2 + 1
}

// credentialFromRPC 从执行请求里还原凭证。
func credentialFromRPC(rpc pluginapi.ExecutorRequest) (today.Credential, error) {
	if len(rpc.StorageJSON) == 0 {
		return today.Credential{}, core.NewPluginError("missing_credential",
			"executor request carries no credential storage", http.StatusUnauthorized)
	}
	cred, ok := parseCredential(rpc.StorageJSON, rpc.AuthID+".json")
	if !ok {
		return today.Credential{}, core.NewPluginError("invalid_credential",
			"credential is not a today credential", http.StatusUnauthorized)
	}
	if cred.Disabled {
		return today.Credential{}, core.NewPluginError("credential_disabled",
			"today credential is disabled", http.StatusForbidden)
	}
	return cred, nil
}

func (a *App) handleCountTokens(request []byte) ([]byte, error) {
	var rpc executorRPC
	_ = decodeRequest(request, &rpc)
	total := estimateTokens(string(append(append([]byte(nil), rpc.OriginalRequest...), rpc.Payload...)))
	payload, _ := json.Marshal(map[string]any{
		"total_tokens":  total,
		"input_tokens":  total,
		"output_tokens": 0,
		"estimated":     true,
	})
	return core.OKEnvelope(pluginapi.ExecutorResponse{Payload: payload})
}

func emitChunk(callbackID, streamID string, payload []byte) error {
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 {
		return nil
	}
	_, errCall := hostbridge.Call(callbackID, pluginabi.MethodHostStreamEmit, map[string]any{
		"stream_id": streamID,
		"payload":   payload,
	})
	return errCall
}

func closeStream(callbackID, streamID, failure string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	payload := map[string]any{"stream_id": streamID}
	if failure != "" {
		payload["error"] = failure
	}
	_, _ = hostbridge.Call(callbackID, pluginabi.MethodHostStreamClose, payload)
}
