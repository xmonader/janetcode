package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Text after the configuration object is a malformed file, not something
// to drop: a stray brace or a second object pasted below the first (whose
// settings would be silently ignored) is reported like any other syntax
// error.
func TestTrailingDataAfterTheConfigIsAnError(t *testing.T) {
	for _, body := range []string{
		`{"provider":"anthropic"}}`,
		`{"provider":"anthropic"}` + "\n" + `{"model":"claude-opus-5"}`,
		`{"provider":"anthropic"} x`,
	} {
		p := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := load(p, env(map[string]string{"ANTHROPIC_API_KEY": "k"}), Overrides{}); !errors.Is(err, ErrBadConfig) {
			t.Errorf("%q: err = %v, want ErrBadConfig", body, err)
		}
	}
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte("{\"provider\":\"anthropic\"}\n\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := load(p, env(map[string]string{"ANTHROPIC_API_KEY": "k"}), Overrides{}); err != nil {
		t.Fatalf("trailing whitespace refused: %v", err)
	}
}

// The file's provider is compared as it is used, lower-cased and
// trimmed: "OpenRouter" in the file is the openrouter entry, so the
// file's own top-level model stays, rather than being taken for another
// provider's and replaced by that entry's last-used model.
func TestTheFilesModelStaysWithTheFilesProviderWhateverItsCase(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"provider":"OpenRouter","model":"qwen/qwen3-coder","providers":{
	  "openrouter":{"api_key":"k","model":"deepseek/deepseek-v4-flash"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(p, env(nil), Overrides{})
	if err != nil || cfg.Provider != "openrouter" || cfg.Model != "qwen/qwen3-coder" {
		t.Fatalf("provider %q model %q, %v", cfg.Provider, cfg.Model, err)
	}
}
