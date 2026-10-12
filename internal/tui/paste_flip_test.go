package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// CRLF text through tmux without -r and text whose every line break is a
// blank line arrive as the same bytes. The first paste reads them as line
// ends; pasting the same text again at once reads them as blank lines, so
// neither reading is lost.
func TestAmbiguousPasteFlipsOnRepaste(t *testing.T) {
	h := newHarness(t)
	h.m.input.SetValue("note: ")
	h.m.input.CursorEnd()
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p1\r\rp2"), Paste: true}
	h.m.handleKey(paste)
	if got := h.m.input.Value(); got != "note: p1\np2" {
		t.Fatalf("first paste = %q", got)
	}
	h.m.handleKey(paste)
	if got := h.m.input.Value(); got != "note: p1\n\np2" {
		t.Fatalf("second paste = %q, want the blank-line reading in place of the first", got)
	}
	h.m.handleKey(paste)
	if got := h.m.input.Value(); got != "note: p1\np2" {
		t.Fatalf("third paste = %q, want the line-end reading back", got)
	}
	// Anything typed in between makes a paste a new one.
	h.m.input.InsertString(" ")
	h.m.handleKey(paste)
	if got := h.m.input.Value(); got != "note: p1\np2 p1\np2" {
		t.Fatalf("paste after typing = %q", got)
	}
}
