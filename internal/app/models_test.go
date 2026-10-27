package app

import (
	"testing"

	"cpa-today-plugin/internal/today"
)

func TestPublicModelIDRoundTrip(t *testing.T) {
	cases := []string{"deepseek-v4.1-flash", "glm-5.3", "minimax-m31-flash"}
	for _, id := range cases {
		public := PublicModelID(id)
		if public != ModelPrefix+id {
			t.Fatalf("PublicModelID(%q) = %q", id, public)
		}
		if got := StripModelPrefix(public); got != id {
			t.Fatalf("StripModelPrefix(%q) = %q, want %q", public, got, id)
		}
	}
}

func TestStripModelPrefixWithoutPrefix(t *testing.T) {
	if got := StripModelPrefix("glm-5.3"); got != "glm-5.3" {
		t.Fatalf("got %q", got)
	}
	if got := StripModelPrefix("  today-glm-5.3  "); got != "glm-5.3" {
		t.Fatalf("got %q", got)
	}
}

// 上游新增模型时必须能调用，而不是掉进 404：
// 探测到的模型不在硬编码表里也要能用。
func TestModelByIDAllowsUnknownModels(t *testing.T) {
	model, ok := today.ModelByID("brand-new-model")
	if !ok {
		t.Fatal("unknown model should still resolve via model.<id> convention")
	}
	if model.ID != "brand-new-model" {
		t.Fatalf("ID = %q", model.ID)
	}
	if model.OptionID != "model.brand-new-model" {
		t.Fatalf("OptionID = %q", model.OptionID)
	}
}

func TestModelByIDKnownModels(t *testing.T) {
	for _, want := range []string{"deepseek-v4.1-flash", "glm-5.3", "minimax-m31-flash"} {
		model, ok := today.ModelByID(want)
		if !ok {
			t.Fatalf("%q should resolve", want)
		}
		if model.ID != want {
			t.Fatalf("ID = %q, want %q", model.ID, want)
		}
		if model.Title == "" || model.Title == want {
			t.Fatalf("%q should have a friendly title, got %q", want, model.Title)
		}
	}
}

func TestModelByIDEmpty(t *testing.T) {
	if _, ok := today.ModelByID(""); ok {
		t.Fatal("empty name must not resolve")
	}
	if _, ok := today.ModelByID("   "); ok {
		t.Fatal("blank name must not resolve")
	}
}

func TestModelsFromPricingSkipsDisabled(t *testing.T) {
	status := map[string]any{
		"modelOptions": map[string]any{
			"options": []any{
				map[string]any{"id": "model.glm-5.3", "disabled": false,
					"display": map[string]any{"title": "GLM 5.3"}},
				map[string]any{"id": "model.gone", "disabled": true,
					"display": map[string]any{"title": "Gone"}},
			},
		},
	}
	models := modelsFromPricing(status)
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if models[0].ID != "today-glm-5.3" {
		t.Fatalf("ID = %q", models[0].ID)
	}
}

func TestModelsFromPricingEmpty(t *testing.T) {
	if got := modelsFromPricing(map[string]any{}); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}
