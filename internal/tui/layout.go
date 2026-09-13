package tui

import (
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/prompt"
)

// compactMode and tight scale the interface to a short window. Neither is
// remembered: they follow the window, and growing it puts everything back.
func (m *model) compactMode() bool { return m.height > 0 && m.height < 20 }

func (m *model) tight() bool { return m.height > 0 && m.height < 16 }

// menuRows is how many completion rows the menu reserves.
func (m *model) menuRows() int {
	if m.compactMode() {
		return 4
	}
	return menuRows
}

// inputLimit is the tallest the prompt box may grow, which is also the
// height the textarea itself is always given (see layout).
func (m *model) inputLimit() int {
	if m.compactMode() {
		return 3
	}
	return maxInputLines
}

func (m *model) inputHeight() int {
	// Reserved at the cap so wrapping or a permission prompt never
	// moves the transcript. Extra lines scroll inside the box.
	return m.inputLimit()
}

func (m *model) footerHeight() int {
	h := m.inputHeight() + 2 + 1 // border, and the reserved notice row
	if m.pending != nil && m.pending.call.Impact != "" {
		// A second reserved row for the impact line (see
		// permissionImpact): what the pending edit reaches. Claimed only
		// while there is one to show, so a call with nothing to say about
		// its reach costs the layout nothing.
		h++
	}
	if m.pending == nil && !m.denying && len(m.completions()) > 0 {
		// The menu always claims the same height, however many rows it has
		// to show: narrowing it as you type used to resize the viewport on
		// every keystroke, and the transcript flickered above it.
		h += m.menuRows() + 1
	}
	return h
}

// followBranch notices an outside checkout in the reserved row rather
// than appending a lasting transcript line.
func (m *model) followBranch() {
	b, ok := prompt.GitBranch(m.opts.Cwd)
	if !ok || b == "" || b == m.branch {
		return
	}
	m.flashNotice("branch changed: " + m.branch + " → " + b)
	m.branch = b
	m.refresh(false)
}

// gutterCollapseWidth is the terminal width below which the transcript
// gutter collapses to zero. Room to breathe is a luxury a wide terminal
// can afford; a narrow one needs every column for diffs, code and tables,
// so the leanness bias wins there. gutterRestoreWidth is several columns
// above that, so resizing around the threshold does not slam the margin.
const (
	gutterCollapseWidth = 88
	gutterRestoreWidth  = 96
)

// gutter is the left margin the whole interface shares this frame: the
// theme's gutter on a wide terminal, zero on a narrow one. It is applied
// uniformly to the transcript's top-level lines, the header, the status
// line and the input box, so one margin runs down the left of everything.
func (m *model) gutter() int {
	if activeGutter <= 0 {
		return 0
	}
	switch {
	case m.width < gutterCollapseWidth:
		m.gutterOff = true
	case m.width >= gutterRestoreWidth:
		m.gutterOff = false
	}
	if m.gutterOff {
		return 0
	}
	return activeGutter
}

func (m *model) layout() {
	if m.width == 0 {
		return
	}
	// The textarea is always as tall as the box may ever grow, and the
	// box takes the top of what it draws. Sizing it to the current line
	// count instead meant the textarea scrolled on the keystroke that
	// added a line (its height was still the old one when it decided)
	// and never scrolled back, so a two-line prompt showed only its
	// second line and the first line of a paste was invisible.
	m.input.SetHeight(m.inputLimit())
	vh := m.height - 1 - 1 - m.footerHeight() - m.planHeight() // header, status, sticky plan
	if vh < 3 {
		vh = 3
	}
	if !m.ready {
		m.viewport = viewport.New(m.width, vh)
		m.viewport.MouseWheelEnabled = true
		m.ready = true
	} else {
		m.viewport.Width = m.width
		m.viewport.Height = vh
	}
	// The input box is inset by the gutter and drawn two columns narrower
	// than the terminal (its border), so the textarea gets what is left.
	m.input.SetWidth(m.width - 4 - m.gutter())
}

// refresh re-renders the transcript into the viewport, keeping it at the
// bottom while it is pinned (see model.pinned).
func (m *model) refresh(resized bool) {
	if !m.ready {
		return
	}
	if h := m.planHeight(); h != m.planH {
		m.planH = h
		m.layout()
	}
	content := m.transcript.render(renderOpts{
		cwd:      m.opts.Cwd,
		width:    m.width,
		gutter:   m.gutter(),
		resized:  resized,
		pending:  m.pending,
		spinner:  m.spinner.View(),
		verbose:  m.verbose,
		cap:      outputCap(m.viewport.Height),
		preview:  m.previewCall,
		renderer: m.toolRenderer,
		tight:    m.tight(),
		compact:  m.compactMode(),
	})
	if content != m.lastContent || resized {
		// SetContent re-parses every line; skip it when nothing changed
		// (spinner ticks with no running tool, key presses, timers).
		m.viewport.SetContent(content)
		m.lastContent = content
	}
	if m.pinned {
		m.viewport.GotoBottom()
	}
}

// scroll passes a key or mouse event to the viewport and re-reads whether
// the transcript still follows new output.
func (m *model) scroll(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	m.pinned = m.viewport.AtBottom()
	return cmd
}

// repin makes the transcript follow new output again.
func (m *model) repin() {
	m.pinned = true
	m.viewport.GotoBottom()
}
