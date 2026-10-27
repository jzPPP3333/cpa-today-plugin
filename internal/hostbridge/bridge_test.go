package hostbridge

import (
	"encoding/json"
	"errors"
	"testing"
)

// fakeBridge 替换真实宿主回调，用于验证组合层的编解码与载荷注入。
func withFake(t *testing.T, fn Caller) {
	t.Helper()
	Configure(fn)
	t.Cleanup(func() { Configure(nil) })
}

func TestMarshalPayloadInjectsCallbackID(t *testing.T) {
	raw, err := MarshalPayload("cb-1", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if parsed["host_callback_id"] != "cb-1" {
		t.Fatalf("host_callback_id = %v", parsed["host_callback_id"])
	}
	if parsed["k"] != "v" {
		t.Fatalf("original field lost: %v", parsed)
	}
}

func TestMarshalPayloadWithoutCallbackLeavesPayloadAlone(t *testing.T) {
	raw, err := MarshalPayload("", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	if _, exists := parsed["host_callback_id"]; exists {
		t.Fatal("callback id must not be injected when empty")
	}
}

func TestDecodeEnvelopeSuccess(t *testing.T) {
	raw := []byte(`{"ok":true,"result":{"value":42}}`)
	result, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if parsed["value"].(float64) != 42 {
		t.Fatalf("value = %v", parsed["value"])
	}
}

func TestDecodeEnvelopeError(t *testing.T) {
	raw := []byte(`{"ok":false,"error":{"code":"boom","message":"failed"}}`)
	if _, err := DecodeEnvelope(raw); err == nil {
		t.Fatal("expected error for ok=false")
	}
}

func TestDecodeEnvelopeInvalidJSON(t *testing.T) {
	if _, err := DecodeEnvelope([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestCallWithoutCallerConfigured(t *testing.T) {
	Configure(nil)
	if _, err := Call("", "host.log", nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("got %v, want ErrNotConfigured", err)
	}
}

func TestCallRoutesThroughConfiguredCaller(t *testing.T) {
	var seenMethod, seenCallback string
	withFake(t, func(callbackID, method string, payload any) (json.RawMessage, error) {
		seenCallback = callbackID
		seenMethod = method
		return json.RawMessage(`{"ok":true}`), nil
	})
	if _, err := Call("cb-9", "host.http.do", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenCallback != "cb-9" || seenMethod != "host.http.do" {
		t.Fatalf("caller got %q/%q", seenCallback, seenMethod)
	}
}

func TestProxyURLOverride(t *testing.T) {
	SetProxyURL("  http://127.0.0.1:10808  ")
	if got := ProxyURL(); got != "http://127.0.0.1:10808" {
		t.Fatalf("ProxyURL = %q (must be trimmed)", got)
	}
	SetProxyURL("")
	if got := ProxyURL(); got != "" {
		t.Fatalf("ProxyURL = %q, want empty", got)
	}
}

func TestResetForInitReopensGate(t *testing.T) {
	// 模拟宿主重载：shutdown 后再 init 必须能继续发起回调。
	Configure(nil)
	SetProxyURL("")
	WaitForShutdown()
	ResetForInit()
	withFake(t, func(callbackID, method string, payload any) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})
	if _, err := Call("", "host.auth.list", map[string]any{}); err != nil {
		t.Fatalf("callback after ResetForInit failed: %v", err)
	}
}
