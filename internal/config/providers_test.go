package config

import "testing"

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
