package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeminiKeyFromTheEnvironmentSpeaksGemini(t *testing.T) {
	cfg, err := load(filepath.Join(t.TempDir(), "missing.json"), env(map[string]string{"GEMINI_API_KEY": "g", "JANETCODE_PROVIDER": "gemini", "JANETCODE_MODEL": "gemini-3-pro"}), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Active()
	if p.Type != TypeGemini || p.BaseURL != "https://generativelanguage.googleapis.com/v1beta" || !p.KeysFromEnv() {
		t.Fatalf("provider = %+v", p)
	}
}

// An entry saved when the gemini preset was the OpenAI-compatible
// endpoint keeps speaking that protocol; the new type would post native
// requests to a URL that does not take them.
func TestOldGeminiEntryKeepsItsProtocol(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	doc := `{"provider":"gemini","model":"gemini-2.5-pro","providers":{"gemini":{"api_key":"g","base_url":"https://generativelanguage.googleapis.com/v1beta/openai"}}}`
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(p, env(nil), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Active().Type; got != TypeOpenAI {
		t.Fatalf("type = %q", got)
	}
}

func TestResponsesTypeLoadsAndAnUnknownOneNamesTheChoices(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(`{"provider":"r","model":"gpt-5.2","providers":{"r":{"type":"responses","api_key":"k"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := load(p, env(nil), Overrides{}); err != nil || cfg.Active().Type != TypeResponses {
		t.Fatalf("cfg=%v err=%v", cfg, err)
	}
	if err := os.WriteFile(p, []byte(`{"provider":"r","model":"m","providers":{"r":{"type":"grpc","api_key":"k"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(p, env(nil), Overrides{}); err == nil || !strings.Contains(err.Error(), "anthropic, openai, gemini, responses") {
		t.Fatalf("err = %v", err)
	}
}
