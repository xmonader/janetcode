package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/theme"
)

// TestThemeSwitchLiveAndPersisted covers the /theme path: a named theme is
// applied to the live palette, the transcript is restyled, and the choice
// is written to the config file touching only the theme key.
func TestThemeSwitchLiveAndPersisted(t *testing.T) {
	defer applyTheme(theme.Builtins()[0]) // restore the default global palette

	h := newHarness(t)
	set, err := theme.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	h.m.opts.Themes = set

	// A config file with an unrelated key, so persistence must leave it.
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(cfgPath, []byte(`{"provider":"anthropic","theme":"auto"}`), 0o600)
	h.m.opts.ConfigPath = cfgPath
	h.m.opts.Config = &config.Config{Provider: "anthropic", Theme: "auto"}

	h.m.applyThemeChoice("gruvbox", true)

	if activeThemeName != "gruvbox" {
		t.Errorf("active theme = %q, want gruvbox", activeThemeName)
	}
	if h.m.transcript.styleName != "dark" {
		t.Errorf("gruvbox is a dark theme; transcript style = %q", h.m.transcript.styleName)
	}
	if h.m.opts.Config.Theme != "gruvbox" {
		t.Errorf("config theme not updated: %q", h.m.opts.Config.Theme)
	}
	// The file has the new theme and kept the provider key.
	var raw map[string]json.RawMessage
	b, _ := os.ReadFile(cfgPath)
	json.Unmarshal(b, &raw)
	if string(raw["theme"]) != `"gruvbox"` {
		t.Errorf("persisted theme = %s", raw["theme"])
	}
	if _, ok := raw["provider"]; !ok {
		t.Errorf("persistence dropped the provider key: %s", b)
	}

	// An unknown name is refused, not applied.
	h.m.applyThemeChoice("does-not-exist", true)
	if activeThemeName != "gruvbox" {
		t.Errorf("unknown theme changed the palette to %q", activeThemeName)
	}
}

// TestResolveThemeModes checks how a theme name maps to the mode drawn.
func TestResolveThemeModes(t *testing.T) {
	set, _ := theme.Build(nil)
	cases := []struct {
		name         string
		detectedDark bool
		wantName     string
		wantDark     bool
	}{
		{"auto", true, theme.DefaultName, true},
		{"auto", false, theme.DefaultName, false},
		{"dark", false, theme.DefaultName, true},
		{"light", true, theme.DefaultName, false},
		{"gruvbox", false, "gruvbox", true},                 // single-mode dark: pins dark even on a light terminal
		{"solarized-light", true, "solarized-light", false}, // single-mode light: pins light
		{"muted", true, "muted", true},                      // adaptive: follows the terminal
		{"muted", false, "muted", false},
	}
	for _, c := range cases {
		th, dark := resolveTheme(c.name, set, c.detectedDark)
		if th.Name != c.wantName || dark != c.wantDark {
			t.Errorf("resolveTheme(%q, detected=%v) = %q/%v, want %q/%v", c.name, c.detectedDark, th.Name, dark, c.wantName, c.wantDark)
		}
	}
}
