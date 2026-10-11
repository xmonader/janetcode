package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestVimUndoTakesBackAnInsert: u after a plain insert takes back what
// was typed. Only normal-mode edits used to be saved, so u skipped the
// insert and undid the edit before it too.
func TestVimUndoTakesBackAnInsert(t *testing.T) {
	h := harnessWithKeys(t, `{}`, true)
	h.keys("abc")
	h.update(esc)
	vimKeys(h, "x") // "ab"
	vimKeys(h, "A")
	h.keys("def")
	h.update(esc)
	vimKeys(h, "u")
	if v := h.m.input.Value(); v != "ab" {
		t.Fatalf("u after A: %q, want the text before the insert", v)
	}
	vimKeys(h, "u")
	if v := h.m.input.Value(); v != "abc" {
		t.Fatalf("second u: %q, want the text before x", v)
	}
	// A change and what is typed into it are one step, as in vim.
	vimKeys(h, "0cw")
	h.keys("xyz")
	h.update(esc)
	vimKeys(h, "u")
	if v := h.m.input.Value(); v != "abc" {
		t.Fatalf("u after cw: %q", v)
	}
}

// TestVimJAcrossAWrappedLine: j from a line long enough to wrap lands on
// the next line. The cursor used to stop on the wrapped line's second
// screen row, so j did nothing and x then deleted from the wrong line.
func TestVimJAcrossAWrappedLine(t *testing.T) {
	h := harnessWithKeys(t, `{}`, true)
	long := strings.TrimSpace(strings.Repeat("word ", 40))
	h.m.input.SetValue(long + "\nshort")
	h.update(esc)
	vimKeys(h, "ggj")
	if _, pos := h.m.vimText(); pos != len(long)+1 {
		t.Fatalf("j landed at %d, want %d", pos, len(long)+1)
	}
	vimKeys(h, "x")
	if v := h.m.input.Value(); v != long+"\nhort" {
		t.Fatalf("x after j: %q", v)
	}
}

// TestVimCursorStaysOnALine: a motion off the end of a line leaves the
// cursor on its last character, as vim does, so x and D still act.
func TestVimCursorStaysOnALine(t *testing.T) {
	h := harnessWithKeys(t, `{}`, true)
	h.keys("abc def")
	h.update(esc)
	vimKeys(h, "0ww")
	vimKeys(h, "x")
	if v := h.m.input.Value(); v != "abc de" {
		t.Fatalf("x after w off the end: %q", v)
	}
	vimKeys(h, "0wdw")
	vimKeys(h, "x")
	if v := h.m.input.Value(); v != "abc" {
		t.Fatalf("x after dw of the last word: %q", v)
	}
	// ^H moves left in normal mode rather than deleting.
	h.update(tea.KeyMsg{Type: tea.KeyCtrlH})
	if v := h.m.input.Value(); v != "abc" {
		t.Fatalf("ctrl+h in normal mode edited the text: %q", v)
	}
}
