package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSmallPasteWithCRLF: the textarea turns \r and \n each into a line
// break, so a short paste copied with Windows line endings arrived
// double-spaced. Only pastes big enough for a chip used to be cleaned.
func TestSmallPasteWithCRLF(t *testing.T) {
	h := newHarness(t)
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\r\nb\r\nc"), Paste: true})
	if v := h.m.input.Value(); v != "a\nb\nc" {
		t.Fatalf("CRLF paste = %q", v)
	}
	// A paste after a chord's first key is text, not its second key.
	h.inputReset()
	h.update(tea.KeyMsg{Type: tea.KeyCtrlX})
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted"), Paste: true})
	if v := h.m.input.Value(); v != "pasted" || h.m.chord != "" {
		t.Fatalf("paste after ctrl+x: %q, chord %q", v, h.m.chord)
	}
}

// TestUpClimbsAWrappedLineFirst: ↑ on the second screen row of a long
// line moves to its first row, as it does between the lines of a
// multi-line prompt. It used to replace the prompt with a history entry.
func TestUpClimbsAWrappedLineFirst(t *testing.T) {
	h := newHarness(t)
	h.m.hist.remember("old prompt")
	h.keys(strings.Repeat("word ", 40))
	typed := h.m.input.Value()
	rows := h.m.input.LineInfo().Height
	if rows < 2 {
		t.Fatalf("the line did not wrap (%d rows)", rows)
	}
	for range rows - 1 {
		h.update(tea.KeyMsg{Type: tea.KeyUp})
		if v := h.m.input.Value(); v != typed {
			t.Fatalf("↑ on a wrapped line recalled %q", v)
		}
	}
	if h.m.input.LineInfo().RowOffset != 0 {
		t.Fatal("↑ did not climb to the first screen row")
	}
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	if v := h.m.input.Value(); v != "old prompt" {
		t.Fatalf("↑ from the top row = %q", v)
	}
	// ↓ hands the draft back.
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	if v := h.m.input.Value(); v != typed {
		t.Fatalf("↓ back = %q", v)
	}
}

// TestHistoryWalksPastACommand: a recalled "/help" opens the command
// menu, and ↑ used to move the menu's highlight instead of going on to
// the older prompt, so the history could not be walked past a command.
func TestHistoryWalksPastACommand(t *testing.T) {
	h := newHarness(t)
	h.m.hist.remember("first")
	h.m.hist.remember("/help")
	h.m.hist.remember("last")
	up, down := tea.KeyMsg{Type: tea.KeyUp}, tea.KeyMsg{Type: tea.KeyDown}
	h.update(up)
	h.update(up)
	if v := h.m.input.Value(); v != "/help" {
		t.Fatalf("up up = %q", v)
	}
	h.update(up)
	if v := h.m.input.Value(); v != "first" {
		t.Fatalf("up from a recalled command = %q", v)
	}
	h.update(down)
	h.update(down)
	if v := h.m.input.Value(); v != "last" {
		t.Fatalf("down past a recalled command = %q", v)
	}
	// Typing over the recalled text gives the menu its arrows back.
	h.inputReset()
	h.keys("/co")
	c, _ := h.m.menu()
	h.update(down)
	if _, idx := h.m.menu(); len(c) < 2 || idx != 1 {
		t.Fatalf("down in a typed menu: idx %d of %d", idx, len(c))
	}
}

// TestCtrlCLeavesTheHistory: ctrl+c clears a recalled line; the next ↑
// starts from the newest prompt again and esc has no old draft to bring
// back.
func TestCtrlCLeavesTheHistory(t *testing.T) {
	h := newHarness(t)
	h.m.hist.remember("one")
	h.m.hist.remember("two")
	h.keys("draft")
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	h.update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if v := h.m.input.Value(); v != "" {
		t.Fatalf("ctrl+c left %q", v)
	}
	h.update(esc)
	if v := h.m.input.Value(); v != "" {
		t.Fatalf("esc after ctrl+c brought back %q", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	if v := h.m.input.Value(); v != "two" {
		t.Fatalf("up after ctrl+c = %q, want the newest prompt", v)
	}
}

// TestEnterSendsACompleteMention: with the @path typed out in full,
// enter sends. It used to "complete" it to the same path plus a space,
// so the prompt took two presses to go.
func TestEnterSendsACompleteMention(t *testing.T) {
	h := newHarness(t, okResp())
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "README.md"), []byte("hi"), 0o644)
	h.keys("explain @README.md")
	h.enter()
	if !h.m.turn.running {
		t.Fatalf("enter did not send: input %q", h.m.input.Value())
	}
	h.pump(func() bool { return !h.m.turn.running })
	// A partial path is still completed by enter.
	h.keys("explain @READ")
	h.enter()
	if v := h.m.input.Value(); v != "explain @README.md " || h.m.turn.running {
		t.Fatalf("enter on a partial path: %q running=%v", v, h.m.turn.running)
	}
}

// TestEditorCRLFIsOneLineBreak: an editor that saves CRLF line endings
// gave back a double-spaced prompt, because the textarea makes \r and \n
// each a line break.
func TestEditorCRLFIsOneLineBreak(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "prompt.md")
	if err := os.WriteFile(path, []byte("one\r\ntwo\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.m.editorDone(editorDoneMsg{path: path})
	if v := h.m.input.Value(); v != "one\ntwo" {
		t.Fatalf("composer = %q", v)
	}
}

// TestCtrlHTakesAChipWhole: ^H is backspace in some terminals and
// ctrl+backspace in most; it took one character off a paste chip.
func TestCtrlHTakesAChipWhole(t *testing.T) {
	h := newHarness(t)
	h.keys("see ")
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("log line\n", 20)), Paste: true})
	h.update(tea.KeyMsg{Type: tea.KeyCtrlH})
	if v := h.m.input.Value(); v != "see " {
		t.Fatalf("ctrl+h over a chip left %q", v)
	}
}

// TestArgumentMenuFooterSaysEnterSends: an argument menu never takes
// enter (keyEnter sends what is typed), so its footer must not say it
// does.
func TestArgumentMenuFooterSaysEnterSends(t *testing.T) {
	h := newHarness(t)
	h.keys("/undo ")
	if c, _ := h.m.menu(); len(c) == 0 || !h.m.comp.args {
		t.Fatal("/undo offers no argument menu")
	}
	foot := plain(h.m.footerView())
	if strings.Contains(foot, "tab or enter accepts") || !strings.Contains(foot, "enter sends what is typed") {
		t.Fatalf("argument menu footer:\n%s", foot)
	}
}
