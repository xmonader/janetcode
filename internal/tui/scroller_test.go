package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The scroller answers the keys and wheel bubbles' viewport did, stays
// within the content, and draws exactly Width by Height.
func TestScroller(t *testing.T) {
	s := newScroller(10, 4)
	s.MouseWheelEnabled = true
	var lines []string
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	s.SetContent(strings.Join(lines, "\n"))
	s.GotoBottom()
	if s.YOffset != 16 || !s.AtBottom() {
		t.Fatalf("bottom: %d", s.YOffset)
	}
	step := func(msg tea.Msg, want int) {
		t.Helper()
		s, _ = s.Update(msg)
		if s.YOffset != want {
			t.Fatalf("%v: offset %d, want %d", msg, s.YOffset, want)
		}
	}
	step(tea.KeyMsg{Type: tea.KeyPgUp}, 12)
	step(tea.KeyMsg{Type: tea.KeyUp}, 11)
	step(tea.KeyMsg{Type: tea.KeyCtrlU}, 9)
	step(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}, 6)
	step(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonWheelUp}, 6)
	step(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}, 9)
	step(tea.KeyMsg{Type: tea.KeyPgDown}, 13)
	step(tea.KeyMsg{Type: tea.KeyPgDown}, 16)
	step(tea.KeyMsg{Type: tea.KeyPgDown}, 16)
	s.GotoTop()
	step(tea.KeyMsg{Type: tea.KeyUp}, 0)

	s.SetYOffset(3)
	if got := s.View(); got != "line 3    \nline 4    \nline 5    \nline 6    " {
		t.Fatalf("view %q", got)
	}
	// Shorter content moves a view past its end to the bottom, and a view
	// taller than the content is padded to the height.
	s.SetYOffset(16)
	s.SetContent("a\nb")
	if s.YOffset != 0 || s.View() != "a         \nb         \n          \n          " {
		t.Fatalf("after shrink: %d %q", s.YOffset, s.View())
	}
}

// TestScrollerNeverShowsBlankBelowTheEnd: content that shrinks by less
// than a screenful, or a window made taller, used to leave the view where
// it was — past the last screenful, with the last few lines at the top
// and blank rows under them.
func TestScrollerNeverShowsBlankBelowTheEnd(t *testing.T) {
	var lines []string
	for i := range 20 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	s := newScroller(10, 4)
	s.SetContent(strings.Join(lines, "\n"))
	s.SetYOffset(14) // scrolled up two lines from the bottom
	s.SetContent(strings.Join(lines[:16], "\n"))
	if s.YOffset != 12 || !strings.HasPrefix(strings.Split(s.View(), "\n")[3], "line 15") {
		t.Fatalf("after a shrink: offset %d, view %q", s.YOffset, s.View())
	}
	s.Height = 10
	s.SetContent(strings.Join(lines[:16], "\n"))
	if s.YOffset != 6 || !strings.HasPrefix(strings.Split(s.View(), "\n")[9], "line 15") {
		t.Fatalf("after growing: offset %d, view %q", s.YOffset, s.View())
	}
}
