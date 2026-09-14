package tui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/flavor"
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
	if ag := m.opts.Agent; ag != nil && ag.Log != nil {
		if g := agent.ActiveGoal(ag.Log.Events()); g != nil {
			parts = append(parts, fmt.Sprintf("goal %s round %d/%d (/goal pause)", g.ID, g.Rounds, g.MaxRounds))
		}
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
	case m.denying && m.pending != nil:
		return "  " + styleWarn.Bold(true).Render("denied "+m.pending.call.Name) + styleDim.Render(" · tell the model why (optional) · enter sends · esc skips")
	case m.pending != nil:
		return m.permissionNotice()
	case len(m.steering) > 0 && len(m.queued) > 0:
		return fitLine("  "+styleWarn.Render(fmt.Sprintf("steering %d", len(m.steering)))+styleFaint.Render(fmt.Sprintf(" · queued %d · ", len(m.queued)))+styleDim.Render(textfmt.OneLine(m.queued[len(m.queued)-1])), room)
	case len(m.steering) > 0:
		return fitLine("  "+styleWarn.Render("steering ")+styleDim.Render(textfmt.OneLine(m.steering[len(m.steering)-1])), room)
	case len(m.queued) > 0:
		return fitLine("  "+styleFaint.Render(fmt.Sprintf("queued %d · ", len(m.queued)))+styleDim.Render(textfmt.OneLine(m.queued[len(m.queued)-1]))+styleFaint.Render(" · up takes it back"), room)
	case m.notice != "" && time.Since(m.noticeAt) < noticeFor:
		return fitLine("  "+styleDim.Render(m.notice), room)
	}
	return ""
}

// flashNotice puts a one-shot line in the reserved row above the input.
// Queue, steering and a permission prompt take the row while they apply.
func (m *model) flashNotice(s string) {
	m.notice, m.noticeAt = s, time.Now()
}

// waitRe matches the "in 12s" a provider puts at the end of a phase to
// say how long it is about to sleep.
var waitRe = regexp.MustCompile(`(.*\bin )([0-9.]+)(m?s)$`)

// setPhase records what the turn is waiting on. A provider that is about
// to sleep says so once — "retrying 1/2 · rate limited · in 6s" — and
// that line then sat unchanged for six seconds next to a counter running
// the other way, which reads as a frozen screen. The deadline is kept so
// the line can count down to the moment something happens.
func (m *model) setPhase(text string) {
	m.phase, m.phaseUntil = text, time.Time{}
	g := waitRe.FindStringSubmatch(text)
	if g == nil {
		return
	}
	d, err := time.ParseDuration(g[2] + g[3])
	if err != nil || d <= 0 {
		return
	}
	m.phase, m.phaseUntil = g[1], time.Now().Add(d)
}

// phaseText is the phase with its countdown resolved to now.
func (m *model) phaseText() string {
	if m.phaseUntil.IsZero() {
		return flavor.ForPhase(m.phase, time.Now(), m.startedAt)
	}
	left := time.Until(m.phaseUntil)
	if left < 0 {
		left = 0
	}
	if left < time.Minute {
		return fmt.Sprintf("%s%ds", m.phase, int(left.Round(time.Second)/time.Second))
	}
	return m.phase + textfmt.Duration(left.Round(time.Second))
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
	st := styleFaint
	if mode != "ask" {
		st = styleKey
	}
	s := st.Render(mode)
	if ag := m.opts.Agent; ag != nil && ag.Sandbox != nil && ag.Sandbox.Enabled() {
		s += styleFaint.Render(" · sandbox")
	}
	if ws := m.opts.Workspace; ws != nil && !ws.Local() {
		s += styleFaint.Render(" · " + ws.Kind())
	}
	return s + styleFaint.Render(" · ")
}

func (m *model) statusView() string {
	var left string
	switch {
	case m.pending != nil:
		left = styleWarn.Render("● approval needed")
	case m.running && m.tight():
		left = m.spinner.View() + " " + styleDim.Render(m.phaseText())
	case m.running:
		el := textfmt.Duration(time.Since(m.startedAt))
		hint := " · esc to cancel"
		// Only while there is something to promote: a key that does
		// nothing is worse than no key.
		if len(m.opts.Running.List()) > 0 {
			hint += " · ctrl+g backgrounds it"
		}
		left = m.spinner.View() + " " + styleDim.Render(m.phaseText()+" · "+el+hint)
	case m.status != "":
		left = styleDim.Render(m.status)
	default:
		left = styleFaint.Render("ready")
	}
	left = m.statusState() + left
	if live := m.liveWork(); live != "" {
		left += styleWarn.Render(" · " + live)
	}
	// The gauge keeps its place whatever else happens. The scroll hint and
	// the cache rate are next to it only while the status text still has
	// room to say something; the cache rate goes first, then the hint.
	parts := []string{}
	if !m.pinned {
		parts = append(parts, styleWarn.Render("↓ pgdn for latest"))
	}
	// In a short window the cache rate is the first thing to go: the
	// scroll hint and the gauge both say something you must act on.
	if pct, ok := m.opts.Agent.CacheRate(); ok && !m.tight() {
		parts = append(parts, styleFaint.Render(fmt.Sprintf("cache %d%%", pct)))
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
	if !m.running {
		est = m.opts.Agent.EstimateTokens()
	}
	limit := m.opts.Agent.CompactThreshold()
	if limit <= 0 {
		return styleFaint.Render("ctx " + textfmt.Tokens(est))
	}
	pct := est * 100 / limit
	if pct > 100 {
		pct = 100
	}
	style := styleFaint
	switch {
	case pct >= 85:
		style = styleErr
	case pct >= 60:
		style = styleWarn
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
	case m.denying, m.pending != nil:
		return styleBoxWarn
	case m.running:
		return styleBoxBusy
	}
	return styleBoxIdle
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
	case m.denying:
		return fitPhrase(room,
			"Why? the model reads this · enter sends · esc skips",
			"Why? enter sends · esc skips",
			"Why?")
	case m.pending != nil:
		return m.pendingKeys()
	case m.opts.Agent.Settings().Provider == nil:
		return fitPhrase(room, "No provider yet · /connect picks one", "/connect")
	case m.running:
		return fitPhrase(room,
			"Type to queue a message for when this turn ends · esc cancels",
			"Type to queue a message · esc cancels",
			"queue · esc cancels")
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
	if m.pending == nil && !m.denying {
		if c, idx := m.menu(); len(c) > 0 {
			rows := m.menuRows()
			start := 0
			if idx >= rows {
				start = idx - rows + 1
			}
			end := min(len(c), start+rows)
			for i := start; i < end; i++ {
				cmd := c[i]
				mark, name, help := "  ", styleKey.Render(cmd.name), styleFaint.Render(cmd.help)
				if i == idx {
					mark, name, help = styleAccent.Render("▸ "), styleKey.Bold(true).Render(cmd.name), styleDim.Render(cmd.help)
				}
				parts = append(parts, fitLine(mark+name+" "+styleDim.Render(cmd.args)+"  "+help, m.width-1-m.gutter()))
			}
			for i := end - start; i < rows; i++ {
				parts = append(parts, "")
			}
			parts = append(parts, styleFaint.Render(fmt.Sprintf("  ↑↓ choose · %d of %d · tab or enter accepts", idx+1, len(c))))
		}
	}
	box := m.inputBox()
	prompt := styleAccent.Render("❯ ")
	switch {
	case m.denying, m.pending != nil:
		prompt = styleWarn.Render("❯ ")
	case m.running:
		prompt = styleFaint.Render("❯ ")
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
