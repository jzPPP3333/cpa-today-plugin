package core

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

func TestToPluginErrorMapsAuth(t *testing.T) {
	err := ToPluginError(&UpstreamError{Kind: "auth", Msg: "bad token", Status: 401})
	pe, ok := err.(*PluginError)
	if !ok {
		t.Fatalf("got %T", err)
	}
	if pe.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("status = %d", pe.HTTPStatus)
	}
	if pe.Retryable {
		t.Fatal("auth errors must not be retryable")
	}
	if pe.Code != "upstream_auth" {
		t.Fatalf("code = %q", pe.Code)
	}
}

func TestToPluginErrorMapsRateLimit(t *testing.T) {
	pe := ToPluginError(&UpstreamError{Kind: "rate_limit", Msg: "slow down"}).(*PluginError)
	if pe.HTTPStatus != http.StatusTooManyRequests || !pe.Retryable {
		t.Fatalf("got status=%d retryable=%v", pe.HTTPStatus, pe.Retryable)
	}
}

func TestToPluginErrorMapsQuota(t *testing.T) {
	pe := ToPluginError(&UpstreamError{Kind: "quota", Msg: "exhausted"}).(*PluginError)
	if pe.HTTPStatus != http.StatusPaymentRequired {
		t.Fatalf("status = %d", pe.HTTPStatus)
	}
}

func TestToPluginErrorMapsServer(t *testing.T) {
	pe := ToPluginError(&UpstreamError{Kind: "server", Msg: "boom"}).(*PluginError)
	if pe.HTTPStatus != http.StatusBadGateway || !pe.Retryable {
		t.Fatalf("got status=%d retryable=%v", pe.HTTPStatus, pe.Retryable)
	}
}

func TestToPluginErrorPassesThroughPluginError(t *testing.T) {
	original := NewPluginError("custom", "msg", http.StatusNotFound)
	converted := ToPluginError(original)
	if converted != original {
		t.Fatalf("PluginError must pass through unchanged, got %T", converted)
	}
}

func TestToPluginErrorWrapsUnknown(t *testing.T) {
	pe := ToPluginError(errors.New("plain")).(*PluginError)
	if !pe.Retryable || pe.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("got retryable=%v status=%d", pe.Retryable, pe.HTTPStatus)
	}
}

func TestToPluginErrorNil(t *testing.T) {
	if err := ToPluginError(nil); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

func TestOKEnvelopeShape(t *testing.T) {
	raw, err := OKEnvelope(map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if !parsed.OK {
		t.Fatal("ok must be true")
	}
	var result map[string]any
	if err := json.Unmarshal(parsed.Result, &result); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if result["a"].(float64) != 1 {
		t.Fatalf("a = %v", result["a"])
	}
}

func TestErrorEnvelopeFromErrorCarriesStatus(t *testing.T) {
	raw := ErrorEnvelopeFromError(NewPluginError("model_not_found", "no such model", http.StatusNotFound))
	var parsed struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			Message    string `json:"message"`
			HTTPStatus int    `json:"http_status"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("envelope is not JSON: %v", err)
	}
	if parsed.OK {
		t.Fatal("ok must be false")
	}
	if parsed.Error.HTTPStatus != http.StatusNotFound {
		t.Fatalf("http_status = %d", parsed.Error.HTTPStatus)
	}
	if parsed.Error.Code != "model_not_found" {
		t.Fatalf("code = %q", parsed.Error.Code)
	}
}
