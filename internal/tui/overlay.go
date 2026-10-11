package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// overlayAgents / overlayJobs are live panels. Unlike the picker they
// are not snapshots: each View re-reads Task.Children() / Jobs.List().
const (
	overlayAgents     = "agents"
	overlayJobs       = "jobs"
	overlayTodos      = "todos"
	overlayTrajectory = "trajectory"
)

// overlayState is the live panel over the transcript: which panel, the
// highlighted row of its list, and — once a row is opened — the id of
// the pane, where it is scrolled and whether it still follows the tail.
// Unlike the picker these are not snapshots: each View re-reads the
// underlying list, so the highlight is clamped on every key.
type overlayState struct {
	kind   string // live panel over the transcript (overlayAgents); empty is closed
	idx    int    // highlighted row in the list
	child  string // id whose pane is open; empty is the list
	scroll int    // line offset in the open pane
	follow bool   // keep the open pane pinned to the tail
	note   string // kill/error line inside a jobs overlay
	top    int    // first list line shown, when the list is taller than the window
}

// open puts a panel up, at the top of its list.
func (o *overlayState) open(kind string) { *o = overlayState{kind: kind, follow: true} }

// close takes the whole panel down.
func (o *overlayState) close() { o.kind, o.child = "", "" }

// openChild opens one row's pane, at its tail.
func (o *overlayState) openChild(id string) { o.child, o.scroll, o.follow = id, 0, true }

// closeChild goes back to the list.
func (o *overlayState) closeChild() { o.child, o.follow = "", true }

// clampIdx keeps the highlight inside a list that has shrunk since the
// last frame — these panels are live, so it can.
func (o *overlayState) clampIdx(n int) {
	if o.idx >= n {
		o.idx = max(n-1, 0)
	}
}

// listKey moves the highlight over a list of n rows, wrapping. It
// reports whether it moved, so the caller knows to redraw.
func (o *overlayState) listKey(k string, n int) bool {
	d := 0
	switch k {
	case "up", "ctrl+p":
		d = -1
	case "down", "ctrl+n":
		d = 1
	default:
		return false
	}
	if n == 0 {
		return false
	}
	o.idx = (o.idx + d + n) % n
	return true
}

// paneKey scrolls the open pane, a line or a screenful of viewH at a
// time. Scrolling back unpins the tail; scrolling forward does not pin
// it again, because the view re-pins itself once it reaches the end.
func (o *overlayState) paneKey(k string, viewH int) bool {
	switch k {
	case "up", "ctrl+p":
		o.follow = false
		o.scroll = max(o.scroll-1, 0)
	case "down", "ctrl+n":
		o.scroll++
	case "pgup":
		o.follow = false
		o.scroll = max(o.scroll-viewH, 0)
	case "pgdown":
		o.scroll += viewH
	default:
		return false
	}
	return true
}

func (m *model) taskTool() *agent.Task { return m.opts.Agent.TaskTool() }

func (m *model) openOverlay(kind string) {
	m.ov.open(kind)
	m.input.Blur()
}

func (m *model) closeOverlay() {
	m.ov.close()
	m.input.Focus()
	m.refresh(false)
}

// paneHeight is the visible height of an open overlay pane, which is
// how far pgup and pgdown move in one press.
func (m *model) paneHeight() int { return max(m.height-6, 4) }

func (m *model) overlayKids() []agent.ChildInfo {
	if t := m.taskTool(); t != nil {
		return t.Children()
	}
	return nil
}

func (m *model) handleOverlayKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.ov.kind == overlayJobs {
		return m.handleJobsKey(msg), true
	}
	if m.ov.kind == overlayTodos {
		return m.handleTodosKey(msg), true
	}
	if m.ov.kind == overlayTrajectory {
		return m.handleTrajKey(msg), true
	}
	if m.ov.child != "" {
		return m.handleAgentDetailKey(msg), true
	}
	kids := m.overlayKids()
	m.ov.clampIdx(len(kids))
	switch k := keyName(msg); {
	case k == "esc", k == "ctrl+c":
		m.closeOverlay()
	case m.ov.listKey(k, len(kids)):
		m.refresh(false)
	case k == "enter":
		if len(kids) > 0 {
			m.ov.openChild(kids[m.ov.idx].ID)
			m.refresh(false)
		}
	case k == "k":
		if len(kids) > 0 {
			m.cancelOverlayChild(kids[m.ov.idx].ID)
		}
	}
	return nil, true
}

func (m *model) handleAgentDetailKey(msg tea.KeyMsg) tea.Cmd {
	switch k := keyName(msg); {
	case k == "esc":
		m.ov.closeChild()
		m.refresh(false)
	case k == "ctrl+c":
		m.closeOverlay()
	case m.ov.paneKey(k, m.paneHeight()):
		m.refresh(false)
	case k == "k":
		m.cancelOverlayChild(m.ov.child)
	}
	return nil
}

func (m *model) cancelOverlayChild(id string) {
	task := m.taskTool()
	if task == nil {
		return
	}
	if err := task.Cancel(id); err != nil {
		m.note(err.Error())
	} else {
		m.note("cancelled " + id)
	}
	m.refresh(false)
}

func (m *model) overlayView(height int) string {
	switch m.ov.kind {
	case overlayAgents:
		if m.ov.child != "" {
			return m.agentDetailView(height)
		}
		return m.agentsListView(height)
	case overlayJobs:
		if m.ov.child != "" {
			return m.jobDetailView(height)
		}
		return m.jobsListView(height)
	case overlayTodos:
		return m.todosOverlayView(height)
	case overlayTrajectory:
		if m.ov.child != "" {
			return m.trajDetailView(height)
		}
		return m.trajListView(height)
	}
	return ""
}

func (m *model) handleTodosKey(msg tea.KeyMsg) tea.Cmd {
	switch k := keyName(msg); {
	case k == "esc", k == "ctrl+c":
		m.closeOverlay()
	case m.ov.paneKey(k, m.paneHeight()):
		m.refresh(false)
	}
	return nil
}

func (m *model) todosOverlayView(height int) string {
	w := min(dialogWidth, m.width-4)
	inner := w - 4
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, inner)) }
	add(m.styles.key.Render("Plan"))
	add(m.styles.dim.Render("this session · live · esc closes"))
	add("")
	todos := m.planTodos()
	if len(todos) == 0 {
		add(m.styles.dim.Render("no plan has been written"))
		add(m.styles.faint.Render("the model starts one with the todo tool when a task has several steps"))
	} else {
		done := 0
		for _, t := range todos {
			if t.Done() {
				done++
			}
		}
		add(m.styles.key.Render(fmt.Sprintf("%d of %d done", done, len(todos))))
		add("")
		for _, t := range todos {
			mark, st := "[ ]", m.styles.dim
			status := "pending"
			switch t.Status {
			case agent.TodoDone:
				mark, st, status = "[x]", m.styles.ok, "completed"
			case agent.TodoDoing:
				mark, st, status = "[>]", m.styles.warn, "in_progress"
			}
			line := st.Render(mark) + " " + t.Text + "  " + m.styles.faint.Render(status)
			if len(t.BlockedBy) > 0 && !t.Done() {
				line += m.styles.faint.Render("  after " + strings.Join(t.BlockedBy, "; "))
			}
			add(line)
		}
	}
	add("")
	add(m.styles.faint.Render("esc close · the sticky strip under the header stays while a plan exists"))
	viewH := height
	maxScroll := max(len(lines)-viewH, 0)
	if m.ov.scroll > maxScroll {
		m.ov.scroll = maxScroll
	}
	if m.ov.scroll < 0 {
		m.ov.scroll = 0
	}
	end := min(len(lines), m.ov.scroll+viewH)
	shown := lines[m.ov.scroll:end]
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(shown, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) agentsListView(height int) string {
	w := min(dialogWidth, m.width-4)
	inner := w - 4
	fit := func(s string) string { return truncateLine(s, inner) }
	head := []string{
		fit(m.styles.key.Render("Subagents")),
		fit(m.styles.dim.Render("this session · live · enter opens the transcript · ctrl+t")),
		"",
	}
	foot := []string{fit(m.styles.faint.Render(`↑↓ choose · enter open · k cancel · esc close · /agents cancel <id>`))}
	kids := m.overlayKids()
	if len(kids) == 0 {
		head = append(head,
			fit(m.styles.dim.Render("no subagents yet")),
			fit(m.styles.faint.Render("the model starts one with the task tool · /agents new <brief> starts one now")))
		return m.listBox(height, w, head, nil, foot)
	}
	if m.ov.idx >= len(kids) {
		m.ov.idx = len(kids) - 1
	}
	items := make([][]string, len(kids))
	for i, k := range kids {
		state, stateStyle := "done", m.styles.ok
		if !k.Done {
			state, stateStyle = "running", m.styles.warn
		}
		age := textfmt.Duration(time.Since(k.Started))
		mark := "  "
		if i == m.ov.idx {
			mark = m.styles.accent.Render("▸ ")
		}
		lines := []string{fit(mark + fmt.Sprintf("%s  %s  %s  %s",
			m.styles.key.Render(k.ID),
			stateStyle.Render(fmt.Sprintf("%-7s", state)),
			m.styles.faint.Render(age),
			k.Description))}
		if k.Last != "" {
			label := "last"
			if !k.Done {
				label = "now"
			}
			lines = append(lines, fit("    "+m.styles.dim.Render(label+": "+k.Last)))
		}
		if k.Usage.InputTokens+k.Usage.OutputTokens > 0 {
			lines = append(lines, fit("    "+m.styles.faint.Render(fmt.Sprintf("tokens in=%s out=%s",
				textfmt.Tokens(k.Usage.InputTokens), textfmt.Tokens(k.Usage.OutputTokens)))))
		}
		items[i] = append(lines, "")
	}
	return m.listBox(height, w, head, items, foot)
}

// listBox draws a list panel: head, the items, foot, in the dialog box,
// centred. The box does not grow past height: when the items are taller
// than the room left, a window of them is shown that keeps the
// highlighted item (m.ov.idx) whole, with a line above and below saying
// what is out of sight. Clipping the whole box instead, as these panels
// did, cut the bottom off — the highlight walked off the screen with the
// key line under it.
func (m *model) listBox(height, w int, head []string, items [][]string, foot []string) string {
	var body []string
	selStart, selEnd := 0, 0
	for i, it := range items {
		if i == m.ov.idx {
			selStart, selEnd = len(body), len(body)+len(it)
		}
		body = append(body, it...)
	}
	room := height - m.styles.dialog.GetVerticalFrameSize() - len(head) - len(foot)
	if len(body) > room {
		win := max(room-2, 1) // two lines say what is above and below
		m.ov.top = min(m.ov.top, selStart)
		if selEnd > m.ov.top+win {
			m.ov.top = min(selEnd-win, selStart)
		}
		m.ov.top = max(min(m.ov.top, len(body)-win), 0)
		end := min(m.ov.top+win, len(body))
		above, below := "", ""
		if m.ov.top > 0 {
			above = m.styles.faint.Render(fmt.Sprintf("  ↑ %d more lines", m.ov.top))
		}
		if end < len(body) {
			below = m.styles.faint.Render(fmt.Sprintf("  ↓ %d more lines", len(body)-end))
		}
		body = append(append([]string{above}, body[m.ov.top:end]...), below)
	} else {
		m.ov.top = 0
	}
	lines := append(append(append([]string{}, head...), body...), foot...)
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) agentDetailView(height int) string {
	task := m.taskTool()
	if task == nil {
		m.ov.child = ""
		return m.agentsListView(height)
	}
	log, ok := task.ChildLog(m.ov.child)
	if !ok {
		m.ov.child = ""
		return m.agentsListView(height)
	}
	w := max(m.width-2, 20)
	state, stateStyle := "done", m.styles.ok
	if !log.Done {
		state, stateStyle = "running", m.styles.warn
	}
	head := fmt.Sprintf("%s  %s  %s  %s",
		m.styles.key.Render(log.ID),
		stateStyle.Render(state),
		m.styles.faint.Render(textfmt.Duration(time.Since(log.Started))),
		log.Description)
	help := m.styles.faint.Render("esc back to the list · k cancel · ↑↓ / pgup pgdn scroll · ctrl+c close")
	body := m.renderChildSteps(log, w)
	viewH := height - 3
	if viewH < 1 {
		viewH = 1
	}
	maxScroll := max(len(body)-viewH, 0)
	if m.ov.follow {
		m.ov.scroll = maxScroll
	}
	if m.ov.scroll > maxScroll {
		m.ov.scroll = maxScroll
		m.ov.follow = true
	}
	if m.ov.scroll < 0 {
		m.ov.scroll = 0
	}
	end := min(len(body), m.ov.scroll+viewH)
	shown := body[m.ov.scroll:end]
	for len(shown) < viewH {
		shown = append(shown, "")
	}
	parts := []string{truncateLine(head, w), help, ""}
	parts = append(parts, shown...)
	return clipHeight(strings.Join(parts, "\n"), height)
}

func (m *model) renderChildSteps(log agent.ChildLog, width int) []string {
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, width)) }
	wrap := func(s string) {
		for _, raw := range strings.Split(s, "\n") {
			add(raw)
		}
	}
	if len(log.Steps()) == 0 {
		if log.Last != "" {
			add(m.styles.dim.Render("now: " + log.Last))
		} else {
			add(m.styles.dim.Render("waiting for the first model call"))
		}
		return lines
	}
	for _, st := range log.Steps() {
		switch st.Kind {
		case "prompt":
			add(m.styles.user.Render("❯ ") + st.Text)
		case "think":
			first, _, _ := strings.Cut(st.Text, "\n")
			add(m.styles.faint.Render("· " + first))
		case "text":
			wrap(st.Text)
			lines = append(lines, "")
		case "tool":
			var mark string
			switch {
			case st.Pending:
				mark = m.styles.warn.Render("…")
			case st.OK:
				mark = m.styles.ok.Render("✓")
			default:
				mark = m.styles.err.Render("✗")
			}
			add(mark + " " + m.styles.tool.Render(st.Name) + " " + m.styles.dim.Render(st.Text))
			if !st.Pending && st.Result != "" {
				lines = append(lines, clipBody(m.styles, st.Result, width, 8, 4)...)
			}
			lines = append(lines, "")
		case "now":
			add(m.styles.warn.Render("now: ") + st.Text)
		}
	}
	return lines
}

// clipBody keeps the head and tail of a tool result so a failed build
// still shows the error, not only the compiler preamble.
func clipBody(sty *styles, s string, width, head, tail int) []string {
	raw := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(raw) == 0 {
		return nil
	}
	take := func(lines []string) []string {
		out := make([]string, len(lines))
		for i, l := range lines {
			out[i] = truncateLine("  "+l, width)
		}
		return out
	}
	if len(raw) <= head+tail+1 {
		return take(raw)
	}
	out := take(raw[:head])
	out = append(out, sty.faint.Render(fmt.Sprintf("  … %d more lines", len(raw)-head-tail)))
	return append(out, take(raw[len(raw)-tail:])...)
}

func (m *model) overlayJobList() []tool.Job {
	if m.opts.Jobs == nil {
		return nil
	}
	return m.opts.Jobs.List()
}

func (m *model) handleJobsKey(msg tea.KeyMsg) tea.Cmd {
	if m.ov.child != "" {
		return m.handleJobDetailKey(msg)
	}
	jobs := m.overlayJobList()
	m.ov.clampIdx(len(jobs))
	switch k := keyName(msg); {
	case k == "esc", k == "ctrl+c":
		m.closeOverlay()
	case m.ov.listKey(k, len(jobs)):
		m.refresh(false)
	case k == "enter":
		if len(jobs) > 0 {
			m.ov.openChild(strconv.Itoa(jobs[m.ov.idx].ID))
			m.refresh(false)
		}
	case k == "k":
		if len(jobs) > 0 {
			m.killOverlayJob(jobs[m.ov.idx].ID)
		}
	}
	return nil
}

func (m *model) handleJobDetailKey(msg tea.KeyMsg) tea.Cmd {
	switch k := keyName(msg); {
	case k == "esc":
		m.ov.closeChild()
		m.refresh(false)
	case k == "ctrl+c":
		m.closeOverlay()
	case m.ov.paneKey(k, m.paneHeight()):
		m.refresh(false)
	case k == "k":
		if id, err := strconv.Atoi(m.ov.child); err == nil {
			m.killOverlayJob(id)
		}
	}
	return nil
}

func (m *model) killOverlayJob(id int) {
	if m.opts.Jobs == nil {
		m.ov.note = "no jobs"
		m.refresh(false)
		return
	}
	if err := m.opts.Jobs.Kill(id); err != nil {
		m.ov.note = err.Error()
	} else {
		m.ov.note = fmt.Sprintf("killed job %d", id)
	}
	m.refresh(false)
}

func (m *model) jobsListView(height int) string {
	w := min(dialogWidth, m.width-4)
	inner := w - 4
	fit := func(s string) string { return truncateLine(s, inner) }
	head := []string{
		fit(m.styles.key.Render("Jobs")),
		fit(m.styles.dim.Render("this session · live · enter tails the log")),
	}
	if m.ov.note != "" {
		head = append(head, fit(m.styles.warn.Render(m.ov.note)))
	}
	head = append(head, "")
	foot := []string{fit(m.styles.faint.Render("↑↓ choose · enter open · k kill · esc close"))}
	jobs := m.overlayJobList()
	if len(jobs) == 0 {
		head = append(head,
			fit(m.styles.dim.Render("no background jobs")),
			fit(m.styles.faint.Render("the model starts one with bash background:true (a dev server, a long build)")))
		return m.listBox(height, w, head, nil, foot)
	}
	if m.ov.idx >= len(jobs) {
		m.ov.idx = len(jobs) - 1
	}
	items := make([][]string, len(jobs))
	for i, j := range jobs {
		state, stateStyle := "done", m.styles.ok
		if !j.Done {
			state, stateStyle = "running", m.styles.warn
		}
		mark := "  "
		if i == m.ov.idx {
			mark = m.styles.accent.Render("▸ ")
		}
		items[i] = []string{
			fit(mark + fmt.Sprintf("%s  %s  %s",
				m.styles.key.Render(fmt.Sprintf("job %d", j.ID)),
				stateStyle.Render(fmt.Sprintf("%-7s", state)),
				j.Command)),
			fit("    " + m.styles.faint.Render(j.Summary())),
			"",
		}
	}
	return m.listBox(height, w, head, items, foot)
}

func (m *model) jobDetailView(height int) string {
	id, err := strconv.Atoi(m.ov.child)
	if err != nil || m.opts.Jobs == nil {
		m.ov.child = ""
		return m.jobsListView(height)
	}
	job, ok := m.opts.Jobs.Get(id)
	if !ok {
		m.ov.child = ""
		return m.jobsListView(height)
	}
	w := max(m.width-2, 20)
	state, stateStyle := "done", m.styles.ok
	if !job.Done {
		state, stateStyle = "running", m.styles.warn
	}
	head := fmt.Sprintf("%s  %s  %s",
		m.styles.key.Render(fmt.Sprintf("job %d", job.ID)),
		stateStyle.Render(state),
		job.Command)
	help := m.styles.faint.Render("esc back · ↑↓ / pgup pgdn scroll · k kill · ctrl+c close")
	if m.ov.note != "" {
		help = m.styles.warn.Render(m.ov.note) + " · " + help
	}
	out, _ := m.opts.Jobs.Output(id, 0)
	var body []string
	if strings.TrimSpace(out) == "" {
		body = []string{m.styles.dim.Render("no output yet")}
	} else {
		body = strings.Split(strings.TrimRight(out, "\n"), "\n")
		for i, l := range body {
			body[i] = truncateLine(l, w)
		}
	}
	viewH := height - 3
	if viewH < 1 {
		viewH = 1
	}
	maxScroll := max(len(body)-viewH, 0)
	if m.ov.follow {
		m.ov.scroll = maxScroll
	}
	if m.ov.scroll > maxScroll {
		m.ov.scroll = maxScroll
		m.ov.follow = true
	}
	if m.ov.scroll < 0 {
		m.ov.scroll = 0
	}
	end := min(len(body), m.ov.scroll+viewH)
	shown := body[m.ov.scroll:end]
	for len(shown) < viewH {
		shown = append(shown, "")
	}
	parts := []string{truncateLine(head, w), help, ""}
	parts = append(parts, shown...)
	return clipHeight(strings.Join(parts, "\n"), height)
}

// jobStdinTimeout bounds /jobs stdin: how long the window waits for a
// job to take a line before it gives up and says so.
var jobStdinTimeout = 3 * time.Second

// jobsCommand opens the live jobs overlay, or kills one by number.
func (m *model) jobsCommand(args []string) {
	if len(args) >= 2 && args[0] == "stdin" {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			m.transcript.errorf("usage: /jobs stdin <n> <text>")
			return
		}
		if m.opts.Jobs == nil {
			m.transcript.errorf("background jobs are not available")
			return
		}
		text := strings.Join(args[2:], " ")
		// This runs on the UI goroutine, so it is bounded: a job that
		// never reads its stdin fills the pipe, and an unbounded write
		// into a full pipe froze the whole window — /jobs kill, the
		// rescue for a wedged job, could no longer even be typed.
		ctx, cancel := context.WithTimeout(m.life, jobStdinTimeout)
		defer cancel()
		if err := m.opts.Jobs.Write(ctx, n, text); err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		m.note(fmt.Sprintf("wrote to job %d", n))
		return
	}
	if len(args) == 2 && args[0] == "kill" {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			m.transcript.errorf("usage: /jobs kill <n>")
			return
		}
		if m.opts.Jobs == nil {
			m.transcript.errorf("background jobs are not available")
			return
		}
		if err := m.opts.Jobs.Kill(n); err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		m.note(fmt.Sprintf("killed job %d", n))
		return
	}
	m.openOverlay(overlayJobs)
	jobs := m.overlayJobList()
	switch {
	case len(args) == 1:
		m.ov.child = args[0]
		if n, err := strconv.Atoi(args[0]); err == nil {
			for i, j := range jobs {
				if j.ID == n {
					m.ov.idx = i
					break
				}
			}
		}
	case len(jobs) == 1:
		m.ov.child = strconv.Itoa(jobs[0].ID)
	}
}
