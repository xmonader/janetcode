package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderLabelNamesWhereOpenAIPoints(t *testing.T) {
	for url, want := range map[string]string{
		"":                             "openai",
		"https://api.openai.com/v1":    "openai",
		"https://openrouter.ai/api/v1": "openrouter",
		"https://api.deepseek.com/v1":  "deepseek",
		"http://localhost:11434/v1":    "ollama",
		"http://gpu-box:8000/v1":       "gpu-box:8000",
	} {
		if got := (Provider{Type: "openai", BaseURL: url}).Label("openai"); got != want {
			t.Errorf("Label(%q) = %q, want %q", url, got, want)
		}
	}
	if got := (Provider{BaseURL: "https://openrouter.ai/api/v1"}).Label("mine"); got != "mine" {
		t.Errorf("a named provider was relabelled: %q", got)
	}
}

func TestUseEnvKeyAndUpdateProviderKeepDefault(t *testing.T) {
	env := func(k string) string {
		if k == "MY_KEY" {
			return "a, b"
		}
		return ""
	}
	var p Provider
	if err := p.UseEnvKey("NOPE", env); err == nil {
		t.Fatal("unset variable accepted")
	}
	if err := p.UseEnvKey("MY_KEY", env); err != nil || p.KeyEnv != "MY_KEY" || len(p.Keys()) != 2 || !p.KeysFromEnv() {
		t.Fatalf("p = %+v, %v", p, err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Update(path, "first", Provider{Type: "openai", BaseURL: "http://x/v1"}, "m1"); err != nil {
		t.Fatal(err)
	}
	if err := UpdateProvider(path, "mine", p, "m2"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var saved struct {
		Provider  string
		Model     string
		Providers map[string]Provider
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Provider != "first" || saved.Model != "m1" || saved.Providers["mine"].KeyEnv != "MY_KEY" || len(saved.Providers["mine"].Keys()) != 0 {
		t.Fatalf("file = %s", data)
	}
}
