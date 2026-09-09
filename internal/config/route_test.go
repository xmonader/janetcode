package config

import "testing"

func routeConfig() *Config {
	return &Config{
		Provider: "dashscope-coding",
		Model:    "qwen3.7-plus",
		Providers: map[string]Provider{
			"dashscope-coding": {Type: "openai", BaseURL: "https://coding-intl.dashscope.aliyuncs.com/v1"},
			"zhipu":            {Type: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4"},
			"house":            {Type: "openai", BaseURL: "https://llm.internal/v1", Models: []string{"house-large"}},
		},
		Routes: map[string]string{"special-7b": "house", "kimi": "house"},
	}
}

func TestRouteForFindsTheProvider(t *testing.T) {
	c := routeConfig()
	for _, tc := range []struct{ model, want, why string }{
		{"glm-5", "zhipu", "the family names the vendor"},
		{"glm-4.6", "zhipu", "a family prefix, not an exact name"},
		{"house-large", "house", "the provider lists it"},
		{"special-7b", "house", "an explicit route"},
		{"kimi-k2.5", "house", "a route on a prefix beats the family"},
		{"qwen3.7-plus", "dashscope-coding", "the session's own model"},
		{"", "dashscope-coding", "no model means the active provider"},
		{"something-nobody-has-heard-of", "dashscope-coding", "an unknown name falls back to the active provider"},
	} {
		got, ok := c.RouteFor(tc.model)
		if !ok || got != tc.want {
			t.Fatalf("%s: %q routed to %q (ok=%v), want %q", tc.why, tc.model, got, ok, tc.want)
		}
	}
}

// A family whose provider is configured under a different name is still
// found, by its endpoint.
func TestRouteForMatchesByEndpoint(t *testing.T) {
	c := routeConfig()
	delete(c.Providers, "zhipu")
	c.Providers["my-glm"] = Provider{Type: "openai", BaseURL: "https://open.bigmodel.cn/api/paas/v4/"}
	if got, ok := c.RouteFor("glm-5"); !ok || got != "my-glm" {
		t.Fatalf("glm-5 routed to %q (ok=%v)", got, ok)
	}
}

func TestFamily(t *testing.T) {
	for model, want := range map[string]string{
		"glm-5": "zhipu", "GLM-4.6": "zhipu", "claude-opus-5": "anthropic",
		"gpt-5": "openai", "deepseek-v3": "deepseek", "grok-4": "xai",
		"qwen3.7-plus": "dashscope", "nothing-known": "",
	} {
		if got := Family(model); got != want {
			t.Fatalf("Family(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestRoutableModels(t *testing.T) {
	c := routeConfig()
	got := c.RoutableModels()
	byName := map[string]ModelRoute{}
	for _, r := range got {
		byName[r.Model] = r
	}
	if byName["house-large"].Provider != "house" || byName["special-7b"].Provider != "house" {
		t.Fatalf("routes = %+v", got)
	}
	if byName["qwen3.7-plus"].Provider != "dashscope-coding" {
		t.Fatalf("the session's model is missing: %+v", got)
	}
	// Sorted, so the list reads the same way twice.
	for i := 1; i < len(got); i++ {
		if got[i-1].Model > got[i].Model {
			t.Fatalf("not sorted: %+v", got)
		}
	}
}

func TestRouteForWithNothingConfigured(t *testing.T) {
	var c Config
	if _, ok := c.RouteFor("glm-5"); ok {
		t.Fatal("routed with no providers configured")
	}
}
