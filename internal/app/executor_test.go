package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildPromptSingleUser(t *testing.T) {
	got := buildPrompt([]chatMessage{{Role: "user", Content: "你好"}}, 0)
	if got != "你好" {
		t.Fatalf("got %q, want 你好", got)
	}
}

func TestBuildPromptFlattensContentBlocks(t *testing.T) {
	content := []any{
		map[string]any{"type": "text", "text": "第一段"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}},
		map[string]any{"type": "text", "text": "第二段"},
	}
	got := buildPrompt([]chatMessage{{Role: "user", Content: content}}, 0)
	if got != "第一段\n第二段" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildPromptWithSystemAndHistory(t *testing.T) {
	msgs := []chatMessage{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "问题一"},
		{Role: "assistant", Content: "回答一"},
		{Role: "user", Content: "问题二"},
	}
	got := buildPrompt(msgs, 0)
	// contextTurns=0 只保留当前一轮。
	if strings.Contains(got, "问题一") {
		t.Fatalf("history should be dropped when contextTurns=0: %q", got)
	}
	if !strings.Contains(got, "[系统指令]") || !strings.Contains(got, "问题二") {
		t.Fatalf("missing system or current message: %q", got)
	}
}

func TestBuildPromptKeepsHistory(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "问题一"},
		{Role: "assistant", Content: "回答一"},
		{Role: "user", Content: "问题二"},
	}
	got := buildPrompt(msgs, 2)
	if !strings.Contains(got, "问题一") || !strings.Contains(got, "[历史对话]") {
		t.Fatalf("history missing: %q", got)
	}
}

func TestBuildPromptEmpty(t *testing.T) {
	if got := buildPrompt(nil, 0); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestCompletionPayloadShape(t *testing.T) {
	raw := completionPayload("today-deepseek-v4.1-flash", "答案")
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if parsed["object"] != "chat.completion" {
		t.Fatalf("object = %v", parsed["object"])
	}
	if parsed["model"] != "today-deepseek-v4.1-flash" {
		t.Fatalf("model = %v", parsed["model"])
	}
	choices, ok := parsed["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("choices = %v", parsed["choices"])
	}
	choice := choices[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	msg := choice["message"].(map[string]any)
	if msg["content"] != "答案" || msg["role"] != "assistant" {
		t.Fatalf("message = %v", msg)
	}
}

func TestStreamChunkPayloadShape(t *testing.T) {
	var parsed map[string]any
	if err := json.Unmarshal(streamChunkPayload("m", "增量"), &parsed); err != nil {
		t.Fatalf("chunk is not JSON: %v", err)
	}
	if parsed["object"] != "chat.completion.chunk" {
		t.Fatalf("object = %v", parsed["object"])
	}
	delta := parsed["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	if delta["content"] != "增量" {
		t.Fatalf("delta = %v", delta)
	}
}

func TestFinishChunkCarriesStopReason(t *testing.T) {
	var parsed map[string]any
	if err := json.Unmarshal(finishChunkPayload("m"), &parsed); err != nil {
		t.Fatalf("finish chunk is not JSON: %v", err)
	}
	choice := parsed["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
}

func TestEstimateTokensNeverZeroForText(t *testing.T) {
	if got := estimateTokens("hello world"); got <= 0 {
		t.Fatalf("estimate = %d, want > 0", got)
	}
	if got := estimateTokens(""); got != 0 {
		t.Fatalf("empty estimate = %d, want 0", got)
	}
}
