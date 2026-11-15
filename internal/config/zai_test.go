package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZAICodingPlanEnvDetection(t *testing.T) {
	for _, name := range []string{"ZAI_CODING_PLAN_API_KEY", "ZAI_API_KEY", "ZAI_CODING_PLAN"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{name: "test-key, other-key"}), Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			p := cfg.Active()
			if cfg.Provider != zaiCodingPlan || cfg.Model != defaultZAIModel || p.Type != TypeOpenAI || p.BaseURL != "https://api.z.ai/api/coding/paas/v4" {
				t.Fatalf("provider=%s model=%s type=%s url=%s", cfg.Provider, cfg.Model, p.Type, p.BaseURL)
			}
			if !p.KeysFromEnv() || p.KeyEnv != name || len(p.Keys()) != 2 || p.Keys()[0] != "test-key" {
				t.Fatal("environment credentials not loaded")
			}
			if route, _ := cfg.RouteFor("glm-4.7"); route != zaiCodingPlan {
				t.Fatalf("GLM routed to %s", route)
			}
		})
	}
}

func TestZAICodingPlanAliasesAndPrecedence(t *testing.T) {
	for _, alias := range []string{"zai-coding-plan", "zai_coding_plan", "ZAI_CODING_PLAN"} {
		t.Run(alias, func(t *testing.T) {
			cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{
				"ANTHROPIC_API_KEY": "anthropic-test", "ZAI_API_KEY": "general-test", "ZAI_CODING_PLAN_API_KEY": "plan-test",
			}), Overrides{Provider: alias, Model: "glm-4.7"})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Provider != zaiCodingPlan || cfg.Model != "glm-4.7" || cfg.Active().APIKey != "plan-test" {
				t.Fatal("explicit selection or plan key priority lost")
			}
			pre, ok := FindPreset(alias)
			if !ok || pre.Name != zaiCodingPlan {
				t.Fatal("preset alias not recognized")
			}
		})
	}
	cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"ANTHROPIC_API_KEY": "a", "ZAI_API_KEY": "z"}), Overrides{})
	if err != nil || cfg.Provider != "anthropic" {
		t.Fatal("Z.ai changed existing default-provider priority")
	}
}

func TestZAICodingPlanConfigAndEnvKeyStayPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"provider":"ZAI_CODING_PLAN","model":"glm-4.7","providers":{"ZAI_CODING_PLAN":{"api_key":"file-key","base_url":"https://custom.example/coding","key_env":"MY_ZAI_KEY"}},"routes":{"glm-":"ZAI_CODING_PLAN"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, env(map[string]string{"ZAI_API_KEY": "general-key", "MY_ZAI_KEY": "private-test-value"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "glm-4.7" || cfg.Active().BaseURL != "https://custom.example/coding" || cfg.Active().APIKey != "private-test-value" {
		t.Fatal("explicit model, URL or key_env lost")
	}
	if got, _ := cfg.RouteFor("glm-5"); got != zaiCodingPlan {
		t.Fatalf("alias route = %s", got)
	}
	if err := Update(path, cfg.Provider, cfg.Active(), cfg.Model); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-test-value") || strings.Contains(string(data), "general-key") {
		t.Fatal("environment secret written to config")
	}
	reloaded, err := load(path, env(nil), Overrides{})
	if err != nil || reloaded.Active().APIKey != "file-key" {
		t.Fatal("normalizing the provider alias lost the saved fallback key")
	}
}

func TestZAICodingPlanMissingKeyNamesEnvironment(t *testing.T) {
	_, err := load(filepath.Join(t.TempDir(), "missing.json"), env(nil), Overrides{Provider: "ZAI_CODING_PLAN"})
	if !errors.Is(err, ErrNoCredentials) || !strings.Contains(err.Error(), "ZAI_CODING_PLAN_API_KEY") {
		t.Fatalf("err = %v", err)
	}
}
