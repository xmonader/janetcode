package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
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

// inputLimit is the tallest the prompt box may grow.
func (m *model) inputLimit() int {
	if m.compactMode() {
		return 3
	}
	return maxInputLines
}

// composerLines is how many display rows the given content takes at the
// given width, using the textarea's own word-wrap rather than a bare
// "\n" count — a line that wraps at the current width grows the box
// exactly as an explicit newline would. Bubbles' textarea has no
// exported "wrapped line count of arbitrary text" call, so each
// "\n"-delimited paragraph is measured on a scratch textarea configured
// like the real one (no prompt, no line numbers, same width) and its
// LineInfo().Height — the real wrap the box would show — is summed; an
// empty paragraph still costs one row, which is why an empty value (one
// empty paragraph) comes back as 1 rather than 0.
func composerLines(value string, width int) int {
	if width <= 0 {
		return minInputLines
	}
	scratch := textarea.New()
	scratch.Prompt = ""
	scratch.ShowLineNumbers = false
	scratch.SetWidth(width)
	total := 0
	for _, line := range strings.Split(value, "\n") {
		scratch.SetValue(line)
		total += scratch.LineInfo().Height
	}
	if total < minInputLines {
		total = minInputLines
	}
	return total
}

// inputHeight is how tall the prompt box is drawn right now: the
// textarea's content at its current width, clamped to [1, inputLimit()].
// It is a pure function of the input's current value and width — called
// again after every keystroke, never cached — so it is always the
// content AS IT STANDS AFTER the key that changed it was applied, which
// is what keeps this out of the trap layout() describes: the textarea
// itself is never resized to this value (see layout()), only the footer
// reservation and the number of the textarea's own rendered lines that
// are shown (see footerView) are.
func (m *model) inputHeight() int {
	n := composerLines(m.input.Value(), m.input.Width())
	if lim := m.inputLimit(); n > lim {
		n = lim
	}
	return n
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
	m.notice.flash("branch changed: " + m.branch + " → " + b)
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
	if m.styles.gutter <= 0 {
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
	return m.styles.gutter
}

func (m *model) layout() {
	if m.width == 0 {
		return
	}
	// The textarea itself is always as tall as the box may ever grow.
	// Sizing it to the current line count instead meant the textarea
	// scrolled on the keystroke that added a line (its internal
	// scrolling decides where to show the cursor using whatever height
	// was set before that keystroke was applied, and there is no
	// exported way to make it redecide afterwards with the grown
	// height) and never scrolled back, so a two-line prompt showed only
	// its second line and the first line of a paste was invisible.
	//
	// So the visible growth is done differently: the textarea is kept
	// pinned at inputLimit() here, forever, and footerView instead draws
	// only the top inputHeight() of its rendered lines (see
	// composerLines and footerView) — which is the actual content
	// whenever it fits, since the textarea only scrolls once the
	// content exceeds inputLimit(), exactly the case where inputHeight
	// equals inputLimit and nothing is cropped.
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
		cwd:       m.opts.Cwd,
		width:     m.width,
		gutter:    m.gutter(),
		resized:   resized,
		pending:   m.pending,
		spinner:   m.spinner.View(),
		verbose:   m.verbose,
		cap:       outputCap(m.viewport.Height),
		preview:   m.previewCall,
		renderer:  m.toolRenderer,
		tight:     m.tight(),
		compact:   m.compactMode(),
		noMermaid: m.opts.Agent != nil && m.opts.Agent.Off["mermaid"],
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
