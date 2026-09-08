package config

import (
	"github.com/xmonader/janetcode/internal/llm"

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
	if cfg.Provider != "anthropic" || cfg.Model != DefaultModel || cfg.MaxTokens != DefaultMaxTokens || cfg.Providers["anthropic"].APIKey != "sk" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !cfg.ThinkingEnabled() {
		t.Fatal("thinking should default on")
	}
}

func TestProviderInferredFromKey(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"OPENAI_API_KEY": "k", "JANETCODE_MODEL": "m"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "openai" || cfg.Model != "m" {
		t.Fatalf("cfg = %+v", cfg)
	}
	// Both keys present: Anthropic stays the default.
	cfg, err = load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"OPENAI_API_KEY": "k", "ANTHROPIC_API_KEY": "a"}), Overrides{})
	if err != nil || cfg.Provider != "anthropic" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	// An explicit provider is never overridden by inference.
	if _, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"OPENAI_API_KEY": "k", "JANETCODE_PROVIDER": "anthropic"}), Overrides{}); err == nil || !strings.Contains(err.Error(), "no API key") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingKeyErrors(t *testing.T) {
	if _, err := load(filepath.Join(t.TempDir(), "x.json"), env(nil), Overrides{}); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("err = %v", err)
	}
	if _, err := load(filepath.Join(t.TempDir(), "x.json"), env(map[string]string{"OPENAI_API_KEY": "k"}), Overrides{Provider: "openai"}); err == nil || !strings.Contains(err.Error(), "needs a model") {
		t.Fatalf("err = %v", err)
	}
	if _, err := load(filepath.Join(t.TempDir(), "x.json"), env(map[string]string{"ANTHROPIC_API_KEY": "k"}), Overrides{Provider: "nosuch"}); err == nil || !strings.Contains(err.Error(), "unknown provider") {
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
	if cfg.Provider != "openai" || cfg.Model != "flag-model" || cfg.Providers["openai"].BaseURL != "http://env" || cfg.Providers["openai"].APIKey != "filekey" {
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
	if err != nil || cfg.CompactAt != 80000 || cfg.Theme != "auto" || cfg.Effort != "high" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestPriceFor(t *testing.T) {
	c := &Config{Prices: map[string]llm.Price{"claude": {Input: 1}, "claude-opus": {Input: 2}, "exact-model": {Input: 3}}}
	if p := c.PriceFor("exact-model"); p == nil || p.Input != 3 {
		t.Fatalf("exact = %+v", p)
	}
	if p := c.PriceFor("claude-opus-5"); p == nil || p.Input != 2 {
		t.Fatalf("longest prefix = %+v", p)
	}
	if p := c.PriceFor("claude-haiku"); p == nil || p.Input != 1 {
		t.Fatalf("short prefix = %+v", p)
	}
	if p := c.PriceFor("gpt"); p != nil {
		t.Fatalf("unknown = %+v", p)
	}
}

func TestProvidersMapMultipleKeysAndPresets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"providers":{
	  "openrouter":{"api_key":"k1","api_keys":["k2","k1"],"model":"deepseek/deepseek-v4-flash"},
	  "ollama":{}}}`), 0o600)
	cfg, err := load(p, env(nil), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	// Inferred: no anthropic key, first configured provider with keys.
	if cfg.Provider != "openrouter" || cfg.Model != "deepseek/deepseek-v4-flash" {
		t.Fatalf("provider/model = %s/%s", cfg.Provider, cfg.Model)
	}
	a := cfg.Active()
	if a.Type != "openai" || a.BaseURL != "https://openrouter.ai/api/v1" || strings.Join(a.Keys(), ",") != "k1,k2" {
		t.Fatalf("active = %+v", a)
	}
	// A keyless local preset is fine; a keyless hosted one is not.
	cfg, err = load(p, env(nil), Overrides{Provider: "ollama", Model: "llama3"})
	if err != nil || cfg.Active().BaseURL != "http://localhost:11434/v1" || cfg.Active().NeedsKey() {
		t.Fatalf("ollama: %+v %v", cfg, err)
	}
	if _, err := load(p, env(nil), Overrides{Provider: "groq", Model: "x"}); err == nil || !strings.Contains(err.Error(), `providers.groq.api_key`) {
		t.Fatalf("groq without key: %v", err)
	}
	// Env keys can be a comma-separated list and replace the file's.
	cfg, err = load(p, env(map[string]string{"OPENAI_API_KEY": "e1, e2", "JANETCODE_PROVIDER": "openai", "JANETCODE_MODEL": "gpt"}), Overrides{})
	if err != nil || strings.Join(cfg.Active().Keys(), ",") != "e1,e2" || cfg.Active().BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("env keys: %+v %v", cfg.Active(), err)
	}
}

func TestProviderOverrideUsesItsOwnModel(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(p, []byte(`{"provider":"anthropic","model":"claude-opus-5","providers":{
	  "anthropic":{"api_key":"a"},
	  "deepseek":{"api_key":"d","model":"deepseek-chat"}}}`), 0o600)
	cfg, err := load(p, env(nil), Overrides{Provider: "deepseek"})
	if err != nil || cfg.Model != "deepseek-chat" {
		t.Fatalf("model = %q, %v", cfg.Model, err)
	}
	cfg, err = load(p, env(nil), Overrides{Provider: "deepseek", Model: "deepseek-reasoner"})
	if err != nil || cfg.Model != "deepseek-reasoner" {
		t.Fatalf("model = %q, %v", cfg.Model, err)
	}
	cfg, err = load(p, env(nil), Overrides{})
	if err != nil || cfg.Model != "claude-opus-5" || cfg.Provider != "anthropic" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func TestUpdateEditsOnlyProviderKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.json")
	// Missing file and directory are created.
	pr := Provider{Type: "openai", BaseURL: "https://openrouter.ai/api/v1"}
	pr.SetKeys([]string{"k1", "k2"})
	if err := Update(p, "openrouter", pr, "m1"); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
	cfg, err := load(p, env(nil), Overrides{})
	if err != nil || cfg.Provider != "openrouter" || cfg.Model != "m1" || strings.Join(cfg.Active().Keys(), ",") != "k1,k2" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	// Other settings and other providers survive; env keys are not written.
	os.WriteFile(p, []byte(`{"max_tokens": 5, "theme": "dark", "providers": {"openrouter": {"type":"openai","api_key":"file-k"}, "x": {"type":"openai","api_key":"xx","base_url":"http://x"}}}`), 0o600)
	cfg, err = load(p, env(map[string]string{"OPENAI_API_KEY": "secret"}), Overrides{Provider: "openai", Model: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Update(p, "openai", cfg.Active(), "gpt-2"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "secret") {
		t.Fatalf("env key persisted: %s", raw)
	}
	cfg, err = load(p, env(nil), Overrides{Provider: "x"})
	if err != nil || cfg.MaxTokens != 5 || cfg.Theme != "dark" || cfg.Active().APIKey != "xx" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
	if _, err := load(p, env(nil), Overrides{}); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("openai entry without key should fail: %v", err)
	}
	// Existing file keys are kept when the in-memory keys came from env.
	cfg, err = load(p, env(map[string]string{"OPENAI_API_KEY": "secret"}), Overrides{Provider: "openrouter", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	e := cfg.Providers["openrouter"]
	if e.APIKey != "file-k" {
		t.Fatalf("openrouter entry = %+v", e)
	}
	an := cfg.Providers["openai"]
	if err := Update(p, "openai", an, "gpt"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p)
	if strings.Contains(string(raw), "secret") || !strings.Contains(string(raw), "file-k") {
		t.Fatalf("file = %s", raw)
	}
}
