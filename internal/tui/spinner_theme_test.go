package tui

import (
	"testing"

	"github.com/xmonader/janetcode/internal/theme"
)

// TestSpinnerStartsInTheThemesAccent: a session opened with a theme drew
// its spinner in the default palette's accent until the first /theme.
func TestSpinnerStartsInTheThemesAccent(t *testing.T) {
	set, err := theme.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	opts := fakeOptions(t)
	opts.Theme, opts.Themes = "gruvbox", set
	h := newHarnessFor(t, opts)
	if got, want := h.m.spinner.Style.GetForeground(), h.m.styles.accent.GetForeground(); got != want {
		t.Fatalf("spinner colour %v, theme accent %v", got, want)
	}
}
