package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestScrollerSelection(t *testing.T) {
	s := newScroller(20, 3)
	s.SetContent("\x1b[31mred line\x1b[0m one\nsecond line\nthird")
	s.setSelection(cell{1, 7}, cell{0, 4}) // reversed on purpose
	if got := s.selectedText(); got != "line one\nsecond" {
		t.Fatalf("text = %q", got)
	}
	view := s.View()
	if !strings.Contains(view, "\x1b[7mline one") || !strings.Contains(view, "\x1b[31mred ") {
		t.Fatalf("highlight lost or styling dropped: %q", view)
	}
	// New lines below keep it; a change under it drops it.
	s.SetContent("\x1b[31mred line\x1b[0m one\nsecond line\nthird\nfourth")
	if !s.sel.on {
		t.Fatal("appending dropped the selection")
	}
	s.SetContent("folded\nsecond line\nthird")
	if s.sel.on {
		t.Fatal("selection kept over changed text")
	}
}

// A drag selects and copies; a click without a move still folds.
func TestMouseDragCopies(t *testing.T) {
	var copied []string
	defer func(f func(string) error) { copyText = f }(copyText)
	copyText = func(s string) error { copied = append(copied, s); return nil }

	here := t.TempDir()
	sess := newSession(t, here)
	m := newModel(Options{Agent: &agent.Agent{Tools: tool.NewRegistry(), Model: "m", Log: sess, Root: here}, Session: sess, Provider: "fake", Cwd: here, Version: "test"})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.transcript.info("alpha bravo charlie\ndelta echo foxtrot")
	m.refresh(false)
	// Find the rows the two lines landed on.
	top := 1 + m.planHeight()
	rowOf := func(word string) int {
		for i := 0; i < m.viewport.Height; i++ {
			if l := m.viewport.YOffset + i; l < len(m.viewport.lines) && strings.Contains(ansi.Strip(m.viewport.lines[l]), word) {
				return top + i
			}
		}
		t.Fatalf("%q not on screen", word)
		return 0
	}
	y1, y2 := rowOf("alpha"), rowOf("delta")
	x1 := strings.Index(ansi.Strip(m.viewport.lines[m.viewport.YOffset+y1-top]), "bravo")
	x2 := strings.Index(ansi.Strip(m.viewport.lines[m.viewport.YOffset+y2-top]), "echo") + len("echo")

	m.Update(tea.MouseMsg{X: x1, Y: y1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m.Update(tea.MouseMsg{X: x2, Y: y2, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	m.Update(tea.MouseMsg{X: x2, Y: y2, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if len(copied) != 1 || !strings.HasPrefix(copied[0], "bravo charlie") || !strings.HasSuffix(copied[0], "delta echo") {
		t.Fatalf("copied = %q", copied)
	}
	if !strings.Contains(m.status, "copied 2 lines") {
		t.Fatalf("status = %q", m.status)
	}
	// A click clears it and copies nothing.
	m.Update(tea.MouseMsg{X: x1, Y: y1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m.Update(tea.MouseMsg{X: x1, Y: y1, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft})
	if m.viewport.sel.on || len(copied) != 1 {
		t.Fatalf("click: sel=%v copied=%d", m.viewport.sel.on, len(copied))
	}
}
