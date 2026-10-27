package app

import "testing"

func TestRegistrationDeclaresConfigFields(t *testing.T) {
	a := New("0.1.0")
	reg := a.Registration()
	if len(reg.Metadata.ConfigFields) == 0 {
		t.Fatal("ConfigFields must be declared for the management UI to render plugin config")
	}

	// 每个声明字段都必须真的能被 DecodeConfig 消费，否则管理端填了也不生效。
	declared := map[string]bool{}
	for _, f := range reg.Metadata.ConfigFields {
		declared[f.Name] = true
		if f.Type == "" {
			t.Errorf("field %q has no type", f.Name)
		}
		if f.Description == "" {
			t.Errorf("field %q has no description", f.Name)
		}
	}

	// 这些键是 DecodeConfig 认识的，必须全部出现在声明里。
	for _, name := range []string{
		"timeout_seconds", "poll_interval_ms", "default_model",
		"context_turns", "reasoning_hint", "proxy_url", "app_base", "api_base",
	} {
		if !declared[name] {
			t.Errorf("config key %q is accepted by DecodeConfig but not declared in ConfigFields", name)
		}
	}
}

func TestRegistrationMetadataIdentity(t *testing.T) {
	reg := New("9.9.9").Registration()
	if reg.Metadata.Name != DisplayName {
		t.Fatalf("Name = %q", reg.Metadata.Name)
	}
	if reg.Metadata.Version != "9.9.9" {
		t.Fatalf("Version = %q", reg.Metadata.Version)
	}
	if reg.Metadata.GitHubRepository == "" {
		t.Fatal("GitHubRepository must be set")
	}
}

func TestRegistrationStableIdentity(t *testing.T) {
	reg := New("0.1.0").Registration()
	if reg.Metadata.Name != "Today" {
		t.Fatalf("Name = %q", reg.Metadata.Name)
	}
}
