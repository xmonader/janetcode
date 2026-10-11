package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Selecting transcript text with the mouse. The interface captures the
// mouse (for the wheel and click-to-fold), which takes the terminal's own
// drag-to-select away; shift+drag still reaches the terminal in most of
// them, but nobody knows that. So a drag over the transcript selects
// here: the cells are highlighted, and on release the text is copied, the
// same text the screen shows. A press and release without a move is
// still a click, and folds or unfolds the entry under it.

// cell is a position in the transcript's content: a line of the
// scroller's text and a display column on it.
type cell struct{ line, col int }

func (a cell) before(b cell) bool { return a.line < b.line || (a.line == b.line && a.col < b.col) }

// selection is the scroller's highlighted range, from..to inclusive of
// from and exclusive of to, in content order.
type selection struct {
	on       bool
	from, to cell
}

func (s *scroller) setSelection(a, b cell) {
	if b.before(a) {
		a, b = b, a
	}
	s.sel = selection{on: true, from: a, to: b}
}

func (s *scroller) clearSelection() { s.sel = selection{} }

// keepSelection drops the selection when the lines it covers changed (a
// fold, an entry redrawn): highlighting the same rows over other text
// would copy something nobody selected. New lines below it, the common
// case while a reply streams, leave it alone.
func (s *scroller) keepSelection(old []string) {
	if !s.sel.on {
		return
	}
	if s.sel.to.line >= len(s.lines) || s.sel.to.line >= len(old) {
		s.clearSelection()
		return
	}
	for i := s.sel.from.line; i <= s.sel.to.line; i++ {
		if s.lines[i] != old[i] {
			s.clearSelection()
			return
		}
	}
}

// lineSpan is the selected column range of line i, ok false when none of
// it is selected.
func (s *scroller) lineSpan(i int) (left, right int, ok bool) {
	if !s.sel.on || i < s.sel.from.line || i > s.sel.to.line {
		return 0, 0, false
	}
	left, right = 0, ansi.StringWidth(s.lines[i])
	if i == s.sel.from.line {
		left = s.sel.from.col
	}
	if i == s.sel.to.line {
		right = s.sel.to.col
	}
	if right <= left {
		return 0, 0, false
	}
	return left, right, true
}

// selectedText is the selection as plain text: styling dropped, trailing
// padding trimmed from each line.
func (s *scroller) selectedText() string {
	if !s.sel.on {
		return ""
	}
	var out []string
	for i := s.sel.from.line; i <= s.sel.to.line && i < len(s.lines); i++ {
		if left, right, ok := s.lineSpan(i); ok {
			out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(s.lines[i], left, right)), " "))
		} else {
			out = append(out, "")
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// highlight draws line i with its selected cells in reverse video,
// keeping the styling of the rest.
func (s *scroller) highlight(i int, line string) string {
	left, right, ok := s.lineSpan(i)
	if !ok {
		return line
	}
	mid := ansi.Strip(ansi.Cut(line, left, right))
	return ansi.Cut(line, 0, left) + "\x1b[7m" + mid + "\x1b[0m" + ansi.Cut(line, right, ansi.StringWidth(line))
}

// drag is the mouse gesture in progress over the transcript.
type drag struct {
	pressed, moved bool
	at             cell
	y              int // the screen row the press was on, for a click
}

// transcriptCell maps a screen position to a content cell, ok false off
// the transcript. Above or below it the row is clamped, so a drag past
// the edge still selects to the first or last line in view.
func (m *model) transcriptCell(x, y int, clamp bool) (cell, bool) {
	if !m.ready || m.mode().modal() {
		return cell{}, false
	}
	row := y - 1 - m.planHeight()
	if row < 0 || row >= m.viewport.Height {
		if !clamp {
			return cell{}, false
		}
		row = min(max(row, 0), m.viewport.Height-1)
	}
	line := m.viewport.YOffset + row
	if line >= len(m.viewport.lines) {
		line = len(m.viewport.lines) - 1
	}
	if line < 0 {
		return cell{}, false
	}
	return cell{line: line, col: max(x, 0)}, true
}

// mouseSelect handles the left button over the transcript, reporting
// whether it used the event.
func (m *model) mouseSelect(msg tea.MouseMsg) bool {
	switch {
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		c, ok := m.transcriptCell(msg.X, msg.Y, false)
		if !ok {
			return false
		}
		m.viewport.clearSelection()
		m.drag = drag{pressed: true, at: c, y: msg.Y}
		m.refresh(false)
		return true
	case msg.Action == tea.MouseActionMotion && m.drag.pressed:
		// Past the top or bottom edge the view scrolls, so a selection can
		// be longer than a screen.
		row := msg.Y - 1 - m.planHeight()
		if row < 0 {
			m.viewport.scrollUp(1)
		} else if row >= m.viewport.Height {
			m.viewport.scrollDown(1)
		}
		c, ok := m.transcriptCell(msg.X, msg.Y, true)
		if !ok {
			return true
		}
		if c != m.drag.at {
			m.drag.moved = true
		}
		if m.drag.moved {
			m.viewport.setSelection(m.drag.at, c)
			m.syncPinned()
			m.refresh(false)
		}
		return true
	case msg.Action == tea.MouseActionRelease && m.drag.pressed:
		d := m.drag
		m.drag = drag{}
		if !d.moved {
			return m.clickTranscript(d.y)
		}
		m.copySelection()
		return true
	}
	return false
}

// copySelection puts the selection on the clipboard: the system's
// helper, else OSC 52, which reaches the local clipboard through ssh in
// the terminals that support it.
func (m *model) copySelection() {
	text := m.viewport.selectedText()
	if text == "" {
		m.viewport.clearSelection()
		m.refresh(false)
		return
	}
	lines := strings.Count(text, "\n") + 1
	copyText := m.copyText
	if copyText == nil {
		copyText = writeClipboardText
	}
	if err := copyText(text); err == nil {
		m.status = fmt.Sprintf("copied %s · click clears the selection", plural(lines, "line"))
	} else if osc52(text) {
		m.status = fmt.Sprintf("sent %s to the terminal's clipboard (OSC 52) · click clears the selection", plural(lines, "line"))
	} else {
		m.status = "could not copy: " + err.Error()
	}
	m.refresh(false)
}

// osc52 asks the terminal to set its clipboard, reporting whether it
// could be sent at all. It goes to stderr in one write, so it cannot land
// inside a frame the renderer is writing to stdout.
func osc52(text string) bool {
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		return false
	}
	_, err := os.Stderr.WriteString("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07")
	return err == nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
