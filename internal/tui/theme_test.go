package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/theme"
)

// TestThemeSwitchLiveAndPersisted covers the /theme path: a named theme is
// applied to the live palette, the transcript is restyled, and the choice
// is written to the config file touching only the theme key.
func TestThemeSwitchLiveAndPersisted(t *testing.T) {
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

	if h.m.styles.themeName != "gruvbox" {
		t.Errorf("active theme = %q, want gruvbox", h.m.styles.themeName)
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
	if h.m.styles.themeName != "gruvbox" {
		t.Errorf("unknown theme changed the palette to %q", h.m.styles.themeName)
	}
}

func TestThemePickerListsMatrixAndPreviewsBeforeApply(t *testing.T) {
	h := newHarness(t)
	set, err := theme.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !set.Has("matrix") {
		t.Fatal("builtin set has no matrix theme")
	}
	h.m.opts.Themes = set
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"theme":"auto"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.m.opts.ConfigPath = cfgPath
	h.m.opts.Config = &config.Config{Theme: "auto"}
	h.m.opts.Theme = "auto"

	h.keys("/theme")
	h.enter()
	if h.m.picker == nil {
		t.Fatal("/theme should open the picker")
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "matrix") {
		t.Fatalf("matrix not listed in /theme:\n%s", v)
	}
	if !strings.Contains(v, "phosphor") {
		t.Fatalf("matrix description missing:\n%s", v)
	}

	h.keys("matrix")
	if h.m.styles.themeName != "matrix" {
		t.Fatalf("highlight should preview matrix, got %q", h.m.styles.themeName)
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(raw), "matrix") {
		t.Fatalf("preview wrote the config:\n%s", raw)
	}

	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.picker != nil {
		t.Fatal("esc should close the picker")
	}
	if h.m.opts.Theme != "auto" {
		t.Fatalf("esc should restore auto, got %q", h.m.opts.Theme)
	}
	if h.m.styles.themeName != theme.DefaultName {
		t.Fatalf("esc should restore the default palette, got %q", h.m.styles.themeName)
	}

	h.keys("/theme")
	h.enter()
	h.keys("matrix")
	h.enter()
	if h.m.picker != nil {
		t.Fatal("enter should close the picker")
	}
	if h.m.styles.themeName != "matrix" {
		t.Fatalf("enter should keep matrix, got %q", h.m.styles.themeName)
	}
	raw, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), `"matrix"`) {
		t.Fatalf("enter should persist matrix:\n%s", raw)
	}
}

// A theme switch must recolour the conversation that is already on
// screen. The per-block markdown cache is keyed by source, width and
// fold state — none of which a switch changes — so without being dropped
// it served every assistant message back in the previous palette.
func TestThemeSwitchDropsTheMarkdownCache(t *testing.T) {
	saved := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(saved)
	lipgloss.SetColorProfile(termenv.TrueColor)

	h := newHarness(t)
	set, err := theme.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	h.m.opts.Themes = set
	h.m.applyThemeChoice("gruvbox", false)
	h.m.transcript.apply(agent.Event{Kind: agent.EvText, Text: "# heading\n\nbody `code` text\n"})
	h.m.transcript.apply(agent.Event{Kind: agent.EvTextBlockDone})
	before := h.m.transcript.render(renderOpts{width: 80})
	if !strings.Contains(before, "\x1b[") {
		t.Fatalf("no colour to compare:\n%q", before)
	}

	h.m.applyThemeChoice("solarized-light", false)
	after := h.m.transcript.render(renderOpts{width: 80})
	if after == before {
		t.Fatalf("the transcript kept the old palette after a theme switch:\n%q", after)
	}
	if plain(after) != plain(before) {
		t.Fatalf("the text changed, not just the colour:\n%q\n%q", plain(before), plain(after))
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

// TestGutterCollapsesUnderThreshold covers part B: the shared left margin
// is present on a wide terminal and gone on a narrow one.
func TestGutterCollapsesUnderThreshold(t *testing.T) {
	h := newHarness(t) // default theme, gutter 2
	h.m.width = 100
	if g := h.m.gutter(); g != 2 {
		t.Errorf("wide terminal gutter = %d, want 2", g)
	}
	h.m.width = 70
	if g := h.m.gutter(); g != 0 {
		t.Errorf("narrow terminal gutter = %d, want 0 (collapsed)", g)
	}
	h.m.width = 88
	if g := h.m.gutter(); g != 0 {
		t.Errorf("after collapse, width 88 gutter = %d, want 0 (hysteresis)", g)
	}
	h.m.width = 95
	if g := h.m.gutter(); g != 0 {
		t.Errorf("after collapse, width 95 gutter = %d, want 0 (still below restore)", g)
	}
	h.m.width = gutterRestoreWidth
	if g := h.m.gutter(); g != 2 {
		t.Errorf("width %d gutter = %d, want 2 (restored)", gutterRestoreWidth, g)
	}

	// The transcript indents its top-level lines by the gutter when it is
	// on, and not when it is off.
	tr := newTranscriptStyled("dark", defaultStyles())
	tr.user("hello there")
	wide := plain(tr.render(renderOpts{width: 100, gutter: 2}))
	if !strings.Contains(wide, "\n  ❯ ") && !strings.HasPrefix(wide, "  ❯ ") {
		t.Errorf("wide render did not indent the prompt line:\n%q", wide)
	}
	tr2 := newTranscriptStyled("dark", defaultStyles())
	tr2.user("hello there")
	narrow := plain(tr2.render(renderOpts{width: 100, gutter: 0}))
	if strings.HasPrefix(narrow, "  ❯") {
		t.Errorf("gutter 0 still indented the prompt line:\n%q", narrow)
	}
}
