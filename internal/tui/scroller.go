package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// scroller is the transcript's viewport: the part of bubbles' viewport
// the interface uses, with the same keys and wheel step. It exists
// because bubbles' SetContent measures the display width of every line
// to support horizontal scrolling, which the transcript never does
// (every line is wrapped to the width): on a 20,000-line session that
// measurement was 30 ms of every frame that changed anything.
type scroller struct {
	Width, Height int
	YOffset       int
	// MouseWheelEnabled turns wheel scrolling on; MouseWheelDelta lines
	// per notch, as bubbles has it.
	MouseWheelEnabled bool
	MouseWheelDelta   int

	lines []string
}

func newScroller(width, height int) scroller {
	return scroller{Width: width, Height: height, MouseWheelDelta: 3}
}

// SetContent replaces the text. A view scrolled past the last screenful
// moves back to it. The test was the last line rather than the last
// screenful, so content that shrank (a fold, a dropped entry) or a window
// grown taller left a view of a few lines over a blank screen until the
// next scroll.
func (s *scroller) SetContent(content string) {
	s.lines = strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if s.YOffset > s.maxYOffset() {
		s.GotoBottom()
	}
}

func (s *scroller) maxYOffset() int { return max(0, len(s.lines)-s.Height) }

// AtBottom reports whether the last line is in view.
func (s *scroller) AtBottom() bool { return s.YOffset >= s.maxYOffset() }

func (s *scroller) atTop() bool { return s.YOffset <= 0 }

// SetYOffset scrolls to line n, kept within the content.
func (s *scroller) SetYOffset(n int) { s.YOffset = min(max(n, 0), s.maxYOffset()) }

func (s *scroller) GotoTop()    { s.SetYOffset(0) }
func (s *scroller) GotoBottom() { s.SetYOffset(s.maxYOffset()) }

func (s *scroller) scrollDown(n int) {
	if !s.AtBottom() && n > 0 && len(s.lines) > 0 {
		s.SetYOffset(s.YOffset + n)
	}
}

func (s *scroller) scrollUp(n int) {
	if !s.atTop() && n > 0 && len(s.lines) > 0 {
		s.SetYOffset(s.YOffset - n)
	}
}

// The keys bubbles' viewport answers to (its DefaultKeyMap), minus the
// horizontal ones.
var scrollKeys = struct {
	pageDown, pageUp, halfDown, halfUp, down, up key.Binding
}{
	pageDown: key.NewBinding(key.WithKeys("pgdown", " ", "f")),
	pageUp:   key.NewBinding(key.WithKeys("pgup", "b")),
	halfDown: key.NewBinding(key.WithKeys("d", "ctrl+d")),
	halfUp:   key.NewBinding(key.WithKeys("u", "ctrl+u")),
	down:     key.NewBinding(key.WithKeys("down", "j")),
	up:       key.NewBinding(key.WithKeys("up", "k")),
}

// Update scrolls for a key or a wheel notch.
func (s scroller) Update(msg tea.Msg) (scroller, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, scrollKeys.pageDown):
			s.scrollDown(s.Height)
		case key.Matches(msg, scrollKeys.pageUp):
			s.scrollUp(s.Height)
		case key.Matches(msg, scrollKeys.halfDown):
			s.scrollDown(s.Height / 2)
		case key.Matches(msg, scrollKeys.halfUp):
			s.scrollUp(s.Height / 2)
		case key.Matches(msg, scrollKeys.down):
			s.scrollDown(1)
		case key.Matches(msg, scrollKeys.up):
			s.scrollUp(1)
		}
	case tea.MouseMsg:
		if !s.MouseWheelEnabled || msg.Action != tea.MouseActionPress || msg.Shift {
			break
		}
		switch msg.Button { //nolint:exhaustive
		case tea.MouseButtonWheelUp:
			s.scrollUp(s.MouseWheelDelta)
		case tea.MouseButtonWheelDown:
			s.scrollDown(s.MouseWheelDelta)
		}
	}
	return s, nil
}

// View is the lines in view, padded and cut to exactly Width by Height,
// as bubbles draws them.
func (s scroller) View() string {
	top := min(max(s.YOffset, 0), len(s.lines))
	bottom := min(max(top+s.Height, top), len(s.lines))
	return lipgloss.NewStyle().
		Width(s.Width).
		Height(s.Height).
		MaxHeight(s.Height).
		MaxWidth(s.Width).
		Render(strings.Join(s.lines[top:bottom], "\n"))
}
