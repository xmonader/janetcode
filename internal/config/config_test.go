package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaultsFromEnvOnly(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"ANTHROPIC_API_KEY": "sk"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "anthropic" || cfg.Model != DefaultModel || cfg.MaxTokens != DefaultMaxTokens || cfg.Anthropic.APIKey != "sk" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !cfg.ThinkingEnabled() {
		t.Fatal("thinking should default on")
	}
}

func TestMissingKeyErrors(t *testing.T) {
	if _, err := load(filepath.Join(t.TempDir(), "x.json"), env(nil), Overrides{}); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("err = %v", err)
	}
	if _, err := load(filepath.Join(t.TempDir(), "x.json"), env(map[string]string{"OPENAI_API_KEY": "k"}), Overrides{Provider: "openai"}); err == nil || !strings.Contains(err.Error(), "needs a model") {
		t.Fatalf("err = %v", err)
	}
	if _, err := load(filepath.Join(t.TempDir(), "x.json"), env(map[string]string{"ANTHROPIC_API_KEY": "k"}), Overrides{Provider: "gemini"}); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("err = %v", err)
	}
	if _, err := load(filepath.Join(t.TempDir(), "x.json"), env(map[string]string{"ANTHROPIC_API_KEY": "k"}), Overrides{Effort: "ultra"}); err == nil || !strings.Contains(err.Error(), "invalid effort") {
		t.Fatalf("err = %v", err)
	}
}

func TestFileEnvOverridePrecedence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"provider":"openai","model":"file-model","max_tokens":123,"thinking":false,
	  "openai":{"api_key":"filekey","base_url":"http://file"},"auto_approve":false,"max_iterations":7}`), 0o600)
	cfg, err := load(p, env(map[string]string{"OPENAI_BASE_URL": "http://env", "JANETCODE_MODEL": "env-model"}), Overrides{Model: "flag-model", AutoApprove: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "openai" || cfg.Model != "flag-model" || cfg.OpenAI.BaseURL != "http://env" || cfg.OpenAI.APIKey != "filekey" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !cfg.AutoApprove || cfg.MaxTokens != 123 || cfg.MaxIterations != 7 || cfg.ThinkingEnabled() {
		t.Fatalf("cfg = %+v", cfg)
	}
	os.WriteFile(p, []byte(`{"bogus":1}`), 0o600)
	if _, err := load(p, env(nil), Overrides{}); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestThinkingHeuristic(t *testing.T) {
	c := &Config{Model: "claude-haiku-4-5"}
	if c.ThinkingEnabled() {
		t.Fatal("haiku should default thinking off")
	}
	on := true
	c.Thinking = &on
	if !c.ThinkingEnabled() {
		t.Fatal("explicit true must win")
	}
}

func TestWriteExampleParses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := WriteExample()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteExample(); err == nil {
		t.Fatal("second write should refuse to overwrite")
	}
	cfg, err := load(p, env(map[string]string{"ANTHROPIC_API_KEY": "k"}), Overrides{})
	if err != nil || cfg.CompactAt != 150000 || cfg.Theme != "auto" || cfg.Effort != "high" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}
