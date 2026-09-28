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
