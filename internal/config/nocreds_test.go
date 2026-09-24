package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A first run with no key still gets a finished configuration. The TUI
// opens on /connect with it -- themes, limits and all -- so returning
// early on the missing key meant the whole session ran with no user
// themes and an unclamped max_tokens, and learned about a malformed
// section only on the run after the key was set.
func TestNoCredentialsStillFinishesTheConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
	  "themes": {"mine": {"base": "nord", "dark": true, "roles": {"accent": "#010203"}}},
	  "theme": "mine",
	  "max_tokens": 0
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(path, env(nil), Overrides{})
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
	if cfg == nil {
		t.Fatal("no config returned with the credentials error")
	}
	if cfg.ThemeSet == nil || !cfg.ThemeSet.Has("mine") {
		t.Errorf("the user's themes were not built: set=%v", cfg.ThemeSet)
	}
	if cfg.MaxTokens != DefaultMaxTokens {
		t.Errorf("max_tokens = %d, want it clamped to %d", cfg.MaxTokens, DefaultMaxTokens)
	}
	if cfg.MaxIterations != DefaultMaxIter {
		t.Errorf("max_iterations = %d, want it clamped to %d", cfg.MaxIterations, DefaultMaxIter)
	}
}

// A file that cannot be used beats credentials that are merely missing:
// /connect can supply a key, it cannot repair a broken section. Before,
// the missing key short-circuited the validation and the broken section
// was not reported at all.
func TestBrokenConfigBeatsMissingCredentials(t *testing.T) {
	for _, c := range []struct{ name, body, want string }{
		{"theme", `{"theme": "nosuchtheme"}`, "unknown theme"},
		{"hooks", `{"hooks": {"bogus_event": []}}`, "hooks"},
		{"sandbox", `{"sandbox": "nosuchprofile"}`, "sandbox"},
	} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := load(path, env(nil), Overrides{})
		if err == nil {
			t.Errorf("%s: no error for a broken config", c.name)
			continue
		}
		if errors.Is(err, ErrNoCredentials) {
			t.Errorf("%s: reported as missing credentials, not as a broken file: %v", c.name, err)
		}
		if !errors.Is(err, ErrBadConfig) {
			t.Errorf("%s: err = %v, want ErrBadConfig", c.name, err)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
		if cfg != nil {
			t.Errorf("%s: a config was returned for a file that cannot be used", c.name)
		}
	}
}

// The first thing a new user sees is this message, printed after
// "janetcode: ". It used to be one 300-character line; it is now the
// problem, then one line per way out, each short enough to read in a
// narrow terminal, and the fastest fix comes first.
func TestNoCredentialsMessageIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	_, err := load(path, env(nil), Overrides{})
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("err = %v, want ErrNoCredentials", err)
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) < 4 {
		t.Fatalf("want the problem plus one line per fix, got %d line(s):\n%s", len(lines), err)
	}
	for _, l := range lines {
		if len(strings.Replace(l, path, "", 1)) > 90 {
			t.Errorf("line is %d chars, want <= 90: %q", len(l), l)
		}
	}
	for i, want := range []string{"no API key", "/connect", "OPENAI_API_KEY", path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q:\n%s", want, err)
		}
		if i == 1 && !strings.Contains(lines[1], want) {
			t.Errorf("the /connect fix is not the first suggestion:\n%s", err)
		}
	}
}
