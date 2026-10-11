package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// An edit made to a recalled prompt survives stepping away and back for
// the rest of the browse, as in a shell; the stored history is untouched
// until the line is sent, and the draft still comes back past the newest.
func TestEditedRecallSurvivesNavigation(t *testing.T) {
	h := newHarness(t)
	h.m.hist.remember("first prompt")
	h.m.hist.remember("second prompt")
	h.keys("dra")
	up := tea.KeyMsg{Type: tea.KeyUp}
	down := tea.KeyMsg{Type: tea.KeyDown}
	h.update(up)
	h.update(up)
	if got := h.m.input.Value(); got != "first prompt" {
		t.Fatalf("after up up: %q", got)
	}
	h.keys(" edited")
	h.update(down)
	if got := h.m.input.Value(); got != "second prompt" {
		t.Fatalf("after down: %q", got)
	}
	h.keys("!")
	h.update(up)
	if got := h.m.input.Value(); got != "first prompt edited" {
		t.Fatalf("the edit was lost: %q", got)
	}
	h.update(down)
	if got := h.m.input.Value(); got != "second prompt!" {
		t.Fatalf("the second edit was lost: %q", got)
	}
	h.update(down)
	if got := h.m.input.Value(); got != "dra" {
		t.Fatalf("draft not restored: %q", got)
	}
	if h.m.hist.entries[0].Text != "first prompt" || h.m.hist.entries[1].Text != "second prompt" {
		t.Fatalf("stored history changed: %+v", h.m.hist.entries)
	}
	// esc ends the browse: the edits go, the draft comes back.
	h.update(up)
	if got := h.m.input.Value(); got != "second prompt!" {
		t.Fatalf("edit gone mid-browse: %q", got)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := h.m.input.Value(); got != "dra" {
		t.Fatalf("esc did not restore the draft: %q", got)
	}
	h.update(up)
	if got := h.m.input.Value(); got != "second prompt" {
		t.Fatalf("a new browse kept the old edits: %q", got)
	}
}
