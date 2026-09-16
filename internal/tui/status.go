package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// verdictBrief reduces a verdict line ("✓ verified: 2 files edited …")
// to its mark and name for the status bar.
func verdictBrief(line string) string {
	if line == "" {
		return ""
	}
	if i := strings.Index(line, ":"); i > 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// filesChangedLastTurn counts distinct files the last turn changed, from
// the provenance record.
func (m *model) filesChangedLastTurn() int {
	if m.opts.Agent == nil || !m.opts.Agent.LastTurnMutated() {
		return 0
	}
	changes := m.opts.Agent.Changes()
	last := 0
	for _, c := range changes {
		if c.Turn > last {
			last = c.Turn
		}
	}
	files := map[string]bool{}
	for _, c := range changes {
		if c.Turn == last && !c.Undone {
			files[c.Path] = true
		}
	}
	return len(files)
}

// goalCache is the goal note the status line last drew, and the size of
// the log it was folded from.
type goalCache struct {
	events int
	line   string
	known  bool
}

// goalLine is the status line's note about the session's goal. Finding it
// means copying the whole event log (Events is a snapshot, for safety
// against the turn goroutine appending) and folding every goal record out
// of it — on a session of two thousand events, 700 KB and a quarter of a
// millisecond, on every frame, to print the same eight words. The answer
// is kept until the log grows, which is the only thing that can change
// it. A log that cannot be counted cheaply is folded every time, as
// before.
func (m *model) goalLine() string {
	ag := m.opts.Agent
	if ag == nil || ag.Log == nil {
		return ""
	}
	counter, ok := ag.Log.(interface{ EventCount() int })
	if ok && m.goal.known && m.goal.events == counter.EventCount() {
		return m.goal.line
	}
	line := ""
	if g := agent.ActiveGoal(ag.Log.Events()); g != nil {
		line = fmt.Sprintf("goal %s round %d/%d (/goal pause)", g.ID, g.Rounds, g.MaxRounds)
	}
	if ok {
		m.goal = goalCache{events: counter.EventCount(), line: line, known: true}
	}
	return line
}

// liveWork names background jobs and subagents still running, for the
// status bar; both are otherwise invisible until they finish.
func (m *model) liveWork() string {
	var parts []string
	if m.opts.Jobs != nil {
		n := 0
		for _, j := range m.opts.Jobs.List() {
			if !j.Done {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, noteJob+textfmt.Plural(n, "job")+" (/jobs)")
		}
	}
	if s := m.goalLine(); s != "" {
		parts = append(parts, s)
	}
	if task := m.taskTool(); task != nil {
		kids := task.Children()
		running := 0
		var last agent.ChildInfo
		for _, k := range kids {
			if !k.Done {
				running++
				last = k
			}
		}
		switch {
		case running > 0 && last.Description != "" && last.Last != "":
			parts = append(parts, textfmt.Plural(running, "subagent")+": "+last.Description+" · "+last.Last+" (/agents)")
		case running > 0:
			parts = append(parts, textfmt.Plural(running, "subagent")+" running (/agents)")
		case len(kids) > 0:
			parts = append(parts, textfmt.Plural(len(kids), "subagent")+" (/agents)")
		}
	}
	return strings.Join(parts, " · ")
}

// footerNotice is the one reserved row above the input: what the prompt
// is waiting for, or what is queued behind the running turn. It is always
// exactly one line, blank when there is nothing.
func (m *model) footerNotice() string {
	room := m.width - 4
	switch {
	case m.mode() == modeDeny:
		return "  " + m.styles.warn.Bold(true).Render("denied "+m.pending.call.Name) + m.styles.dim.Render(" · tell the model why (optional) · enter sends · esc skips")
	case m.pending != nil:
		return m.permissionNotice()
	case len(m.turn.steering) > 0 && len(m.turn.queued) > 0:
		return fitLine("  "+m.styles.warn.Render(fmt.Sprintf("steering %d", len(m.turn.steering)))+m.styles.faint.Render(fmt.Sprintf(" · queued %d · ", len(m.turn.queued)))+m.styles.dim.Render(textfmt.OneLine(m.turn.queued[len(m.turn.queued)-1])), room)
	case len(m.turn.steering) > 0:
		return fitLine("  "+m.styles.warn.Render("steering ")+m.styles.dim.Render(textfmt.OneLine(m.turn.steering[len(m.turn.steering)-1])), room)
	case len(m.turn.queued) > 0:
		// What it is waiting for, and the other thing it could have been:
		// the row is where somebody looks after pressing enter and
		// finding the message has not gone anywhere yet.
		return fitLine("  "+m.styles.faint.Render(fmt.Sprintf("queued %d · ", len(m.turn.queued)))+m.styles.dim.Render(textfmt.OneLine(m.turn.queued[len(m.turn.queued)-1]))+m.styles.faint.Render(" · sends when this turn ends · up takes it back, ctrl+s steers it into this turn"), room)
	case m.notice.live():
		return fitLine("  "+m.styles.dim.Render(m.notice.text), room)
	}
	return ""
}

// statusMode is the compact label for the current permission mode, shown
// on the status line so a shift+tab does not have to dump a paragraph
// into the transcript.
func (m *model) statusMode() string {
	ag := m.opts.Agent
	if ag == nil {
		return "ask"
	}
	switch modes := ag.Settings(); {
	case modes.PlanMode:
		return "PLAN"
	case modes.ReadOnly:
		return "READ-ONLY"
	case modes.AutoApprove:
		return "YOLO"
	case modes.AutoMode:
		return "AUTO"
	case modes.AutoEdits:
		return "EDITS"
	default:
		return "ask"
	}
}

func (m *model) statusModeHint() string {
	switch m.statusMode() {
	case "PLAN":
		return "reads only · writes a plan · /plan approve runs it"
	case "READ-ONLY":
		return "inspect only"
	case "YOLO":
		return "tools run without asking"
	case "AUTO":
		return "edits and known-safe reads run · other commands ask"
	case "EDITS":
		return "file changes go through · commands ask"
	default:
		return "edits and commands ask first"
	}
}

// noteMode puts the mode explanation on the status line instead of in
// the transcript. The mode token itself is always drawn by statusState.
func (m *model) noteMode() {
	m.note(m.statusModeHint())
}

// note puts a one-line acknowledgement on the status line. Toggles,
// titles, forks, copies and "started X" used to append to the
// conversation; they belong here with the mode chrome. Multi-line
// reports (/help, /inspect, /doctor, /btw's answer) stay on the transcript.
func (m *model) note(s string) {
	m.status = strings.TrimSpace(s)
}

// statusState is the persistent left-hand chrome: mode, sandbox, remote
// workspace. It stays on every frame so toggling those does not have to
// print into the conversation.
func (m *model) statusState() string {
	mode := m.statusMode()
	st := m.styles.faint
	if mode != "ask" {
		st = m.styles.key
	}
	s := st.Render(mode)
	// A tier above the provider's own default is a standing choice about
	// what every call costs, so it sits with the mode rather than being
	// something you have to run /effort to remember.
	if ag := m.opts.Agent; ag != nil {
		if e := ag.Settings().Effort; e != "" {
			s += m.styles.faint.Render(" · effort:" + e)
		}
	}
	if ag := m.opts.Agent; ag != nil && ag.Sandbox != nil && ag.Sandbox.Enabled() {
		// The profile is the part that says what is actually confined,
		// so the chrome names it rather than only that there is a box.
		s += m.styles.faint.Render(" · sandbox:" + ag.Sandbox.ProfileName())
	}
	if ws := m.opts.Workspace; ws != nil && !ws.Local() {
		s += m.styles.faint.Render(" · " + ws.Kind())
	}
	return s + m.styles.faint.Render(" · ")
}

func (m *model) statusView() string {
	var left string
	switch {
	case m.pending != nil:
		left = m.styles.warn.Render("● approval needed")
	case m.turn.running && m.tight():
		left = m.spinner.View() + " " + m.styles.dim.Render(m.turn.phaseText())
	case m.turn.running:
		el := textfmt.Duration(time.Since(m.turn.startedAt))
		hint := " · esc to cancel"
		// Only while there is something to promote: a key that does
		// nothing is worse than no key.
		if len(m.opts.Running.List()) > 0 {
			hint += " · ctrl+g backgrounds it"
		}
		left = m.spinner.View() + " " + m.styles.dim.Render(m.turn.phaseText()+" · "+el+hint)
	case m.status != "":
		left = m.styles.dim.Render(m.status)
	default:
		left = m.styles.faint.Render("ready")
	}
	left = m.statusState() + left
	if live := m.liveWork(); live != "" {
		left += m.styles.warn.Render(" · " + live)
	}
	// The gauge keeps its place whatever else happens. The scroll hint and
	// the cache rate are next to it only while the status text still has
	// room to say something; the cache rate goes first, then the hint.
	parts := []string{}
	if !m.pinned {
		parts = append(parts, m.styles.warn.Render("↓ pgdn for latest"))
	}
	// In a short window the cache rate is the first thing to go: the
	// scroll hint and the gauge both say something you must act on.
	if pct, ok := m.opts.Agent.CacheRate(); ok && !m.tight() {
		parts = append(parts, m.styles.faint.Render(fmt.Sprintf("cache %d%%", pct)))
	}
	parts = append(parts, m.contextGauge())
	const leastStatus = 12
	for len(parts) > 1 && lipgloss.Width(strings.Join(parts, "  ")) > m.width-leastStatus {
		parts = append(parts[:len(parts)-2], parts[len(parts)-1])
	}
	right := strings.Join(parts, "  ")
	// The status line shares the gutter: its left margin is the gutter, or
	// the single space it has always had when the gutter has collapsed.
	lead := m.gutter()
	if lead < 1 {
		lead = 1
	}
	// The status text yields to the gauge, however little is left of it.
	// Its room counts the margin, the gap and the trailing space: leaving
	// the margin out let the line overflow by the gutter, and the final
	// fit below cut the gauge's figure to "45…".
	if avail := m.width - lipgloss.Width(right) - lead - 2; lipgloss.Width(left) > avail {
		left = fitLine(left, avail)
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - lead - 1
	if gap < 1 {
		gap = 1
	}
	return fitLine(strings.Repeat(" ", lead)+left+strings.Repeat(" ", gap)+right+" ", m.width)
}

// contextGauge shows how full the context window is relative to the
// compaction threshold. It is the one thing on the status line that never
// gives way; below 50 columns it drops the bar and keeps the number.
func (m *model) contextGauge() string {
	// While a turn runs the history belongs to the turn goroutine; the
	// gauge reads the estimate the agent published instead.
	est := m.opts.Agent.TokenEstimate()
	if !m.turn.running {
		est = m.opts.Agent.EstimateTokens()
	}
	limit := m.opts.Agent.CompactThreshold()
	if limit <= 0 {
		return m.styles.faint.Render("ctx " + textfmt.Tokens(est))
	}
	pct := est * 100 / limit
	if pct > 100 {
		pct = 100
	}
	style := m.styles.faint
	switch {
	case pct >= 85:
		style = m.styles.err
	case pct >= 60:
		style = m.styles.warn
	}
	if m.width < 50 {
		return style.Render(fmt.Sprintf("ctx %d%%", pct))
	}
	const cells = 10
	filled := pct * cells / 100
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", cells-filled)
	return style.Render(fmt.Sprintf("ctx %s %d%%", bar, pct))
}

// inputBox is the border the input box wears in the current state; the
// comment in styles.go says what each colour means.
func (m *model) inputBox() lipgloss.Style {
	switch {
	case m.pending != nil:
		return m.styles.boxWarn
	case m.turn.running:
		return m.styles.boxBusy
	}
	return m.styles.boxIdle
}

// placeholder is the hint shown in an empty input box. It names the same
// states the border colours in styles.go do, and is recomputed every frame
// so no transition can leave a stale hint behind.
// fitPhrase picks the longest of several wordings of the same thing that
// fits the space, rather than cutting the longest one mid-phrase. A
// narrow window used to offer "· / for" where it meant "· / for
// commands".
func fitPhrase(room int, options ...string) string {
	for _, o := range options {
		if lipgloss.Width(o) <= room {
			return o
		}
	}
	if len(options) == 0 {
		return ""
	}
	return truncateLine(options[len(options)-1], room)
}

func (m *model) placeholder() string {
	// The box draws "❯ " before the textarea's own width.
	room := m.input.Width() - 2
	switch {
	case m.mode() == modeDeny:
		return fitPhrase(room,
			"Why? the model reads this · enter sends · esc skips",
			"Why? enter sends · esc skips",
			"Why?")
	case m.pending != nil:
		return m.pendingKeys()
	case m.opts.Agent.Settings().Provider == nil:
		return fitPhrase(room, "No provider yet · /connect picks one", "/connect")
	case m.turn.running:
		// Both intents, at every width. Queueing and steering are one
		// keystroke apart and the difference — after this turn, or into
		// it — is invisible unless the box says so, so the shortest
		// wording still names both keys and drops everything else.
		return fitPhrase(room,
			"enter queues a message for when this turn ends · ctrl+s steers it into this one · esc cancels",
			"enter queues for after this turn · ctrl+s steers into it · esc cancels",
			"enter queues · ctrl+s steers · esc cancels",
			"enter queues · ctrl+s steers")
	case m.opts.Agent.Settings().PlanMode:
		return fitPhrase(room,
			"Plan mode: describe the change · /plan approve runs the plan",
			"Plan mode · /plan approve runs it",
			"Plan mode")
	case m.opts.Agent.Settings().AutoMode:
		return fitPhrase(room,
			"Auto: in-workspace edits and known-safe reads run · other commands ask",
			"Auto · edits and known-safe reads run",
			"Auto")
	case m.opts.Agent.Settings().ReadOnly:
		return fitPhrase(room,
			"Read-only: the model can inspect but change nothing",
			"Read-only · nothing is changed",
			"Read-only")
	case m.followup != "":
		return fitPhrase(room, m.followup+" · tab accepts", m.followup)
	}
	return fitPhrase(room, defaultPlaceholder,
		"Ask for a change or a fix · / for commands",
		"/ for commands")
}

func (m *model) footerView() string {
	// One row above the input is always there, blank when there is
	// nothing to say. Notices used to push the prompt down as they
	// arrived and pull it up as they went, so the box moved under the
	// cursor while typing. A permission prompt uses that row and keeps
	// this box, so the transcript does not move.
	parts := []string{m.footerNotice()}
	if imp := m.permissionImpact(); imp != "" {
		parts = append(parts, imp)
	}
	// The command menu and the @path menu are the same menu: same rows,
	// same highlight, same footer line, same height whatever is in them.
	if m.mode() == modePrompt {
		if c, idx := m.menu(); len(c) > 0 {
			rows := m.menuRows()
			start := 0
			if idx >= rows {
				start = idx - rows + 1
			}
			end := min(len(c), start+rows)
			for i := start; i < end; i++ {
				cmd := c[i]
				mark, name, help := "  ", m.styles.key.Render(cmd.name), m.styles.faint.Render(cmd.help)
				if i == idx {
					mark, name, help = m.styles.accent.Render("▸ "), m.styles.key.Bold(true).Render(cmd.name), m.styles.dim.Render(cmd.help)
				}
				parts = append(parts, fitLine(mark+name+" "+m.styles.dim.Render(cmd.args)+"  "+help, m.width-1-m.gutter()))
			}
			for i := end - start; i < rows; i++ {
				parts = append(parts, "")
			}
			parts = append(parts, m.styles.faint.Render(fmt.Sprintf("  ↑↓ choose · %d of %d · tab or enter accepts", idx+1, len(c))))
		}
	}
	box := m.inputBox()
	prompt := m.styles.accent.Render("❯ ")
	switch {
	case m.pending != nil:
		prompt = m.styles.warn.Render("❯ ")
	case m.turn.running:
		prompt = m.styles.faint.Render("❯ ")
	}
	m.input.Placeholder = m.placeholder()
	lines := strings.Split(m.input.View(), "\n")
	if n := m.inputHeight(); n < len(lines) {
		lines = lines[:n] // the rest is the textarea's blank padding
	}
	for i := range lines {
		if i == 0 {
			lines[i] = prompt + lines[i]
		} else {
			lines[i] = "  " + lines[i]
		}
	}
	parts = append(parts, box.Width(m.width-2-m.gutter()).Render(strings.Join(lines, "\n")))
	out := strings.Join(parts, "\n")
	// The whole footer — the notice row, the menu and the input box —
	// shares the gutter with the transcript and header above it.
	if g := m.gutter(); g > 0 {
		out = indent(out, strings.Repeat(" ", g))
	}
	return out
}
