package app

import "testing"

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TimeoutSeconds != 240 {
		t.Fatalf("TimeoutSeconds = %d", cfg.TimeoutSeconds)
	}
	if cfg.DefaultModel != "deepseek-v4.1-flash" {
		t.Fatalf("DefaultModel = %q", cfg.DefaultModel)
	}
	if cfg.Raw == nil {
		t.Fatal("Raw must be initialized")
	}
}

func TestDecodeConfigEmpty(t *testing.T) {
	cfg, err := DecodeConfig(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TimeoutSeconds != 240 {
		t.Fatalf("expected defaults, got %+v", cfg)
	}
}

func TestDecodeConfigOverrides(t *testing.T) {
	raw := []byte("timeout_seconds: 120\npoll_interval_ms: 300\ndefault_model: glm-5.3\ncontext_turns: 3\nproxy_url: http://127.0.0.1:10808\n")
	cfg, err := DecodeConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TimeoutSeconds != 120 {
		t.Fatalf("TimeoutSeconds = %d", cfg.TimeoutSeconds)
	}
	if cfg.PollIntervalMs != 300 {
		t.Fatalf("PollIntervalMs = %d", cfg.PollIntervalMs)
	}
	if cfg.DefaultModel != "glm-5.3" {
		t.Fatalf("DefaultModel = %q", cfg.DefaultModel)
	}
	if cfg.ContextTurns != 3 {
		t.Fatalf("ContextTurns = %d", cfg.ContextTurns)
	}
	if cfg.ProxyURL != "http://127.0.0.1:10808" {
		t.Fatalf("ProxyURL = %q", cfg.ProxyURL)
	}
}

func TestDecodeConfigBaseOverrideTrimsSlash(t *testing.T) {
	cfg, err := DecodeConfig([]byte("app_base: http://mock:1/\napi_base: http://mock:2/\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppBase != "http://mock:1" || cfg.APIBase != "http://mock:2" {
		t.Fatalf("AppBase=%q APIBase=%q", cfg.AppBase, cfg.APIBase)
	}
}

func TestDecodeConfigIgnoresInvalidNumbers(t *testing.T) {
	cfg, err := DecodeConfig([]byte("timeout_seconds: not-a-number\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TimeoutSeconds != 240 {
		t.Fatalf("invalid value must keep default, got %d", cfg.TimeoutSeconds)
	}
}

func TestDecodeConfigKeepsUnknownKeys(t *testing.T) {
	cfg, err := DecodeConfig([]byte("some_future_key: value\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Raw["some_future_key"] != "value" {
		t.Fatalf("unknown key not retained: %+v", cfg.Raw)
	}
}

func TestDecodeConfigHandlesComments(t *testing.T) {
	raw := []byte("# 注释\n time out\n")
	cfg, err := DecodeConfig(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TimeoutSeconds != 240 {
		t.Fatalf("got %d", cfg.TimeoutSeconds)
	}
}
