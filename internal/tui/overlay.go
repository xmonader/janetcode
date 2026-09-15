package tui

import (
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
	overlayAgents = "agents"
	overlayJobs   = "jobs"
	overlayTodos  = "todos"
)

func (m *model) taskTool() *agent.Task { return m.opts.Agent.TaskTool() }

func (m *model) openOverlay(kind string) {
	m.overlay = kind
	m.overlayIdx = 0
	m.overlayChild = ""
	m.overlayScroll = 0
	m.overlayFollow = true
	m.overlayNote = ""
	m.input.Blur()
}

func (m *model) closeOverlay() {
	m.overlay = ""
	m.overlayChild = ""
	m.input.Focus()
	m.refresh(false)
}

func (m *model) overlayKids() []agent.ChildInfo {
	if t := m.taskTool(); t != nil {
		return t.Children()
	}
	return nil
}

func (m *model) handleOverlayKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if m.overlay == overlayJobs {
		return m.handleJobsKey(msg), true
	}
	if m.overlay == overlayTodos {
		return m.handleTodosKey(msg), true
	}
	if m.overlayChild != "" {
		return m.handleAgentDetailKey(msg), true
	}
	kids := m.overlayKids()
	if m.overlayIdx >= len(kids) {
		m.overlayIdx = max(len(kids)-1, 0)
	}
	switch keyName(msg) {
	case "esc", "ctrl+c":
		m.closeOverlay()
	case "up", "ctrl+p":
		if len(kids) > 0 {
			m.overlayIdx = (m.overlayIdx - 1 + len(kids)) % len(kids)
			m.refresh(false)
		}
	case "down", "ctrl+n":
		if len(kids) > 0 {
			m.overlayIdx = (m.overlayIdx + 1) % len(kids)
			m.refresh(false)
		}
	case "enter":
		if len(kids) > 0 {
			m.overlayChild = kids[m.overlayIdx].ID
			m.overlayScroll = 0
			m.overlayFollow = true
			m.refresh(false)
		}
	}
	return nil, true
}

func (m *model) handleAgentDetailKey(msg tea.KeyMsg) tea.Cmd {
	viewH := max(m.height-6, 4)
	switch keyName(msg) {
	case "esc":
		m.overlayChild = ""
		m.overlayFollow = true
		m.refresh(false)
	case "ctrl+c":
		m.closeOverlay()
	case "up", "ctrl+p":
		m.overlayFollow = false
		if m.overlayScroll > 0 {
			m.overlayScroll--
		}
		m.refresh(false)
	case "down", "ctrl+n":
		m.overlayScroll++
		m.refresh(false)
	case "pgup":
		m.overlayFollow = false
		m.overlayScroll = max(m.overlayScroll-viewH, 0)
		m.refresh(false)
	case "pgdown":
		m.overlayScroll += viewH
		m.refresh(false)
	}
	return nil
}

func (m *model) overlayView(height int) string {
	switch m.overlay {
	case overlayAgents:
		if m.overlayChild != "" {
			return m.agentDetailView(height)
		}
		return m.agentsListView(height)
	case overlayJobs:
		if m.overlayChild != "" {
			return m.jobDetailView(height)
		}
		return m.jobsListView(height)
	case overlayTodos:
		return m.todosOverlayView(height)
	}
	return ""
}

func (m *model) handleTodosKey(msg tea.KeyMsg) tea.Cmd {
	viewH := max(m.height-6, 4)
	switch keyName(msg) {
	case "esc", "ctrl+c":
		m.closeOverlay()
	case "up", "ctrl+p":
		if m.overlayScroll > 0 {
			m.overlayScroll--
		}
		m.refresh(false)
	case "down", "ctrl+n":
		m.overlayScroll++
		m.refresh(false)
	case "pgup":
		m.overlayScroll = max(m.overlayScroll-viewH, 0)
		m.refresh(false)
	case "pgdown":
		m.overlayScroll += viewH
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
	if m.overlayScroll > maxScroll {
		m.overlayScroll = maxScroll
	}
	if m.overlayScroll < 0 {
		m.overlayScroll = 0
	}
	end := min(len(lines), m.overlayScroll+viewH)
	shown := lines[m.overlayScroll:end]
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(shown, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) agentsListView(height int) string {
	w := min(dialogWidth, m.width-4)
	inner := w - 4
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, inner)) }
	add(m.styles.key.Render("Subagents"))
	add(m.styles.dim.Render("this session · live · enter opens the transcript · ctrl+t"))
	add("")
	kids := m.overlayKids()
	if len(kids) == 0 {
		add(m.styles.dim.Render("no subagents yet"))
		add(m.styles.faint.Render("the model starts one with the task tool · /agents new <brief> starts one now"))
	} else {
		if m.overlayIdx >= len(kids) {
			m.overlayIdx = len(kids) - 1
		}
		for i, k := range kids {
			state, stateStyle := "done", m.styles.ok
			if !k.Done {
				state, stateStyle = "running", m.styles.warn
			}
			age := textfmt.Duration(time.Since(k.Started))
			mark := "  "
			if i == m.overlayIdx {
				mark = m.styles.accent.Render("▸ ")
			}
			add(mark + fmt.Sprintf("%s  %s  %s  %s",
				m.styles.key.Render(k.ID),
				stateStyle.Render(fmt.Sprintf("%-7s", state)),
				m.styles.faint.Render(age),
				k.Description))
			if k.Last != "" {
				label := "last"
				if !k.Done {
					label = "now"
				}
				add("    " + m.styles.dim.Render(label+": "+k.Last))
			}
			if k.Usage.InputTokens+k.Usage.OutputTokens > 0 {
				add("    " + m.styles.faint.Render(fmt.Sprintf("tokens in=%s out=%s",
					textfmt.Tokens(k.Usage.InputTokens), textfmt.Tokens(k.Usage.OutputTokens))))
			}
			add("")
		}
	}
	add(m.styles.faint.Render(`↑↓ choose · enter open · esc close · /agents new <brief> starts one · /agents <id> jumps`))
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) agentDetailView(height int) string {
	task := m.taskTool()
	if task == nil {
		m.overlayChild = ""
		return m.agentsListView(height)
	}
	log, ok := task.ChildLog(m.overlayChild)
	if !ok {
		m.overlayChild = ""
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
	help := m.styles.faint.Render("esc back to the list · ↑↓ / pgup pgdn scroll · ctrl+c close")
	body := m.renderChildSteps(log, w)
	viewH := height - 3
	if viewH < 1 {
		viewH = 1
	}
	maxScroll := max(len(body)-viewH, 0)
	if m.overlayFollow {
		m.overlayScroll = maxScroll
	}
	if m.overlayScroll > maxScroll {
		m.overlayScroll = maxScroll
		m.overlayFollow = true
	}
	if m.overlayScroll < 0 {
		m.overlayScroll = 0
	}
	end := min(len(body), m.overlayScroll+viewH)
	shown := body[m.overlayScroll:end]
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
	if m.overlayChild != "" {
		return m.handleJobDetailKey(msg)
	}
	jobs := m.overlayJobList()
	if m.overlayIdx >= len(jobs) {
		m.overlayIdx = max(len(jobs)-1, 0)
	}
	switch keyName(msg) {
	case "esc", "ctrl+c":
		m.closeOverlay()
	case "up", "ctrl+p":
		if len(jobs) > 0 {
			m.overlayIdx = (m.overlayIdx - 1 + len(jobs)) % len(jobs)
			m.refresh(false)
		}
	case "down", "ctrl+n":
		if len(jobs) > 0 {
			m.overlayIdx = (m.overlayIdx + 1) % len(jobs)
			m.refresh(false)
		}
	case "enter":
		if len(jobs) > 0 {
			m.overlayChild = strconv.Itoa(jobs[m.overlayIdx].ID)
			m.overlayScroll = 0
			m.overlayFollow = true
			m.refresh(false)
		}
	case "k":
		if len(jobs) > 0 {
			m.killOverlayJob(jobs[m.overlayIdx].ID)
		}
	}
	return nil
}

func (m *model) handleJobDetailKey(msg tea.KeyMsg) tea.Cmd {
	viewH := max(m.height-6, 4)
	switch keyName(msg) {
	case "esc":
		m.overlayChild = ""
		m.overlayFollow = true
		m.refresh(false)
	case "ctrl+c":
		m.closeOverlay()
	case "up", "ctrl+p":
		m.overlayFollow = false
		if m.overlayScroll > 0 {
			m.overlayScroll--
		}
		m.refresh(false)
	case "down", "ctrl+n":
		m.overlayScroll++
		m.refresh(false)
	case "pgup":
		m.overlayFollow = false
		m.overlayScroll = max(m.overlayScroll-viewH, 0)
		m.refresh(false)
	case "pgdown":
		m.overlayScroll += viewH
		m.refresh(false)
	case "k":
		if id, err := strconv.Atoi(m.overlayChild); err == nil {
			m.killOverlayJob(id)
		}
	}
	return nil
}

func (m *model) killOverlayJob(id int) {
	if m.opts.Jobs == nil {
		m.overlayNote = "no jobs"
		m.refresh(false)
		return
	}
	if err := m.opts.Jobs.Kill(id); err != nil {
		m.overlayNote = err.Error()
	} else {
		m.overlayNote = fmt.Sprintf("killed job %d", id)
	}
	m.refresh(false)
}

func (m *model) jobsListView(height int) string {
	w := min(dialogWidth, m.width-4)
	inner := w - 4
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, inner)) }
	add(m.styles.key.Render("Jobs"))
	add(m.styles.dim.Render("this session · live · enter tails the log"))
	if m.overlayNote != "" {
		add(m.styles.warn.Render(m.overlayNote))
	}
	add("")
	jobs := m.overlayJobList()
	if len(jobs) == 0 {
		add(m.styles.dim.Render("no background jobs"))
		add(m.styles.faint.Render("the model starts one with bash background:true (a dev server, a long build)"))
	} else {
		if m.overlayIdx >= len(jobs) {
			m.overlayIdx = len(jobs) - 1
		}
		for i, j := range jobs {
			state, stateStyle := "done", m.styles.ok
			if !j.Done {
				state, stateStyle = "running", m.styles.warn
			}
			mark := "  "
			if i == m.overlayIdx {
				mark = m.styles.accent.Render("▸ ")
			}
			add(mark + fmt.Sprintf("%s  %s  %s",
				m.styles.key.Render(fmt.Sprintf("job %d", j.ID)),
				stateStyle.Render(fmt.Sprintf("%-7s", state)),
				j.Command))
			add("    " + m.styles.faint.Render(j.Summary()))
			add("")
		}
	}
	add(m.styles.faint.Render("↑↓ choose · enter open · k kill · esc close"))
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) jobDetailView(height int) string {
	id, err := strconv.Atoi(m.overlayChild)
	if err != nil || m.opts.Jobs == nil {
		m.overlayChild = ""
		return m.jobsListView(height)
	}
	job, ok := m.opts.Jobs.Get(id)
	if !ok {
		m.overlayChild = ""
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
	if m.overlayNote != "" {
		help = m.styles.warn.Render(m.overlayNote) + " · " + help
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
	if m.overlayFollow {
		m.overlayScroll = maxScroll
	}
	if m.overlayScroll > maxScroll {
		m.overlayScroll = maxScroll
		m.overlayFollow = true
	}
	if m.overlayScroll < 0 {
		m.overlayScroll = 0
	}
	end := min(len(body), m.overlayScroll+viewH)
	shown := body[m.overlayScroll:end]
	for len(shown) < viewH {
		shown = append(shown, "")
	}
	parts := []string{truncateLine(head, w), help, ""}
	parts = append(parts, shown...)
	return clipHeight(strings.Join(parts, "\n"), height)
}

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
		if err := m.opts.Jobs.Write(n, text); err != nil {
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
		m.overlayChild = args[0]
		if n, err := strconv.Atoi(args[0]); err == nil {
			for i, j := range jobs {
				if j.ID == n {
					m.overlayIdx = i
					break
				}
			}
		}
	case len(jobs) == 1:
		m.overlayChild = strconv.Itoa(jobs[0].ID)
	}
}
