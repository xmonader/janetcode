package config

import (
	"path/filepath"
	"testing"
)

func TestOpenCodeEnvironmentAndAliases(t *testing.T) {
	for _, tc := range []struct {
		key, selected string
		goKey, zenKey bool
	}{
		{"OPENCODE_GO_API_KEY", "opencode-go", true, false},
		{"OPENCODE_API_KEY", "opencode-go", true, true},
		{"OPENCODE_ZEN_API_KEY", "opencode-zen", false, true},
	} {
		t.Run(tc.key, func(t *testing.T) {
			cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{tc.key: "test-key"}), Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Provider != tc.selected || cfg.Model != "kimi-k3" {
				t.Fatalf("provider=%s model=%s", cfg.Provider, cfg.Model)
			}
			for name, want := range map[string]bool{"opencode-go": tc.goKey, "opencode-zen": tc.zenKey} {
				p := cfg.Providers[name]
				if (len(p.Keys()) > 0) != want {
					t.Fatalf("%s key detection=%v", name, !want)
				}
				if want && (!p.KeysFromEnv() || p.KeyEnv != tc.key) {
					t.Fatal("lost environment provenance")
				}
			}
		})
	}
	for alias, want := range map[string]string{"OPENCODE_GO": "opencode-go", "opencodego": "opencode-go", "OPENCODE_ZEN": "opencode-zen", "zen": "opencode-zen", "opencode": "opencode-zen"} {
		cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"OPENCODE_API_KEY": "k"}), Overrides{Provider: alias})
		if err != nil || cfg.Provider != want {
			t.Fatalf("alias=%s err=%v", alias, err)
		}
	}
}

func TestOpenCodePlanKeysAndURLsStaySeparate(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{
		"OPENCODE_GO_API_KEY": "go-key", "OPENCODE_API_KEY": "zen-key", "OPENCODE_GO_BASE_URL": "https://go.example/v1", "OPENCODE_BASE_URL": "https://zen.example/v1",
	}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Providers["opencode-go"]; p.APIKey != "go-key" || p.BaseURL != "https://go.example/v1" {
		t.Fatal("Go key or URL lost")
	}
	if p := cfg.Providers["opencode-zen"]; p.APIKey != "zen-key" || p.BaseURL != "https://zen.example/v1" {
		t.Fatal("Zen key or URL lost")
	}
}

func TestOpenCodeQualifiedModelSelection(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"ANTHROPIC_API_KEY": "a", "OPENCODE_GO_API_KEY": "g"}), Overrides{Model: "opencode-go/gpt-5.6-luna"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "opencode-go" || cfg.Model != "gpt-5.6-luna" {
		t.Fatalf("provider=%s model=%s", cfg.Provider, cfg.Model)
	}
	if got, ok := cfg.RouteFor("opencode-go/qwen3.8-flash"); !ok || got != "opencode-go" {
		t.Fatalf("route=%s ok=%v", got, ok)
	}
	_, err = load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"OPENCODE_API_KEY": "k"}), Overrides{Provider: "opencode-go", Model: "opencode-zen/kimi-k3"})
	if err == nil {
		t.Fatal("conflicting provider and model reference accepted")
	}
}
