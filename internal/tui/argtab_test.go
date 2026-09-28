package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestTabCompletesACommandArgument: the /open, /diff, /resume, /model,
// /forget and /undo menus offer whole lines ("/open alpha.txt"), and tab
// appended the row to what was typed, leaving "/open /open alpha.txt".
// The menus that offer a bare word (/jobs kill) must keep working.
func TestTabCompletesACommandArgument(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(filepath.Join(h.m.opts.Cwd, "alpha.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.keys("/open al")
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != "/open alpha.txt " {
		t.Fatalf("tab after /open al = %q", got)
	}
	h.m.input.SetValue("")
	h.keys("/jobs ki")
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != "/jobs kill " {
		t.Fatalf("tab after /jobs ki = %q", got)
	}
}
