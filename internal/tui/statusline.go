package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/statusline"
)

// statusLine is where the user's status line command stands: the lines it
// last printed, drawn under the input, and what they were printed for.
type statusLine struct {
	lines []string
	// running is set while the command runs: one at a time, and a change
	// that lands meanwhile is picked up by the next tick.
	running bool
	// key is the session state the lines were made for (see
	// statusLineKey); a tick that finds it changed runs the command again.
	key string
	ran time.Time
	// failed is the last error, flashed once rather than on every tick.
	failed string
}

type statusLineMsg struct {
	lines []string
	key   string
	err   error
}

// statusLineKey is a fingerprint of what the status line command is told:
// when it changes the command has something new to say. It is cheap
// enough to take on every tick.
func (m *model) statusLineKey() string {
	ag := m.opts.Agent
	s := ag.Settings()
	u := ag.UsageSnapshot()
	_, path := ag.SessionRef()
	return fmt.Sprintf("%s|%s|%d|%d|%d|%s|%s|%t|%s",
		s.Model, s.ModeName(), m.tokensInUse(), u.InputTokens, u.OutputTokens,
		m.branch, path, m.turn.running, m.opts.Cwd)
}

// tokensInUse is the context's size as the gauge shows it: while a turn
// runs the history belongs to the turn goroutine, so the estimate the
// agent published is read instead.
func (m *model) tokensInUse() int {
	if m.turn.running {
		return m.opts.Agent.TokenEstimate()
	}
	return m.opts.Agent.EstimateTokens()
}

// statusLineInput is the session as the command reads it.
func (m *model) statusLineInput() statusline.Input {
	ag := m.opts.Agent
	s := ag.Settings()
	u := ag.UsageSnapshot()
	id, path := ag.SessionRef()
	in := statusline.Input{
		SessionID:      id,
		TranscriptPath: path,
		Cwd:            m.opts.Cwd,
		Version:        m.opts.Version,
		PermissionMode: s.ModeName(),
		Model:          statusline.Model{ID: s.Model, DisplayName: s.Model},
		Workspace:      statusline.Workspace{CurrentDir: m.opts.Cwd, ProjectDir: m.opts.Cwd},
	}
	if m.branch != "" && m.branch != "git" {
		in.Git = &statusline.Git{Branch: m.branch}
	}
	in.ContextWindow.SetUsage(m.tokensInUse(), s.ContextWindow)
	in.ContextWindow.TotalInputTokens = u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
	in.ContextWindow.TotalOutputTokens = u.OutputTokens
	if usd, _, _, known := ag.SessionCost(); known {
		in.Cost = &statusline.Cost{TotalCostUSD: usd}
	}
	return in
}

// runStatusLine starts the command when the session has changed since it
// last ran, or its refresh interval has passed; otherwise it returns nil.
func (m *model) runStatusLine() tea.Cmd {
	c := m.opts.StatusLine
	if c == nil || m.sline.running || m.opts.Agent == nil {
		return nil
	}
	key := m.statusLineKey()
	due := key != m.sline.key
	if r := c.Refresh(); r > 0 && time.Since(m.sline.ran) >= r {
		due = true
	}
	if !due {
		return nil
	}
	m.sline.running, m.sline.ran = true, time.Now()
	in, dir, ctx, cfg := m.statusLineInput(), m.opts.Cwd, m.life, *c
	return func() tea.Msg {
		lines, err := statusline.Run(ctx, cfg, dir, in)
		return statusLineMsg{lines: lines, key: key, err: err}
	}
}

// showStatusLine takes the command's answer. A failure blanks the line,
// as a stale one would claim something no longer true, and says why once.
func (m *model) showStatusLine(msg statusLineMsg) {
	m.sline.running, m.sline.key = false, msg.key
	lines := msg.lines
	if msg.err != nil {
		lines = nil
		if msg.err.Error() != m.sline.failed {
			m.notice.flash(msg.err.Error())
		}
		m.sline.failed = msg.err.Error()
	} else {
		m.sline.failed = ""
	}
	resize := len(lines) != len(m.sline.lines)
	m.sline.lines = lines
	if resize {
		m.layout()
	}
	m.refresh(false)
}

// statusLineView is the command's lines as drawn under the input: its own
// colours when it chose any, else dimmed like the rest of the chrome, and
// each cut to the width.
func (m *model) statusLineView() []string {
	out := make([]string, 0, len(m.sline.lines))
	room := m.width - 2 - m.gutter()
	for _, l := range m.sline.lines {
		if !strings.Contains(l, "\x1b[") {
			l = m.styles.dim.Render(l)
		}
		if lipgloss.Width(l) > room {
			l = fitLine(l, room)
		}
		out = append(out, " "+l)
	}
	return out
}
