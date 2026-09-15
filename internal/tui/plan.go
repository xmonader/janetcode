package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xmonader/janetcode/internal/agent"
)

// The plan strip sits under the header for as long as the model has a
// todo list. It is not an overlay: the conversation stays in view, and
// each rewrite of the plan (a step marked off, a new step added) is
// drawn on the next frame. A short window collapses it to one line; a
// tight one hides it so the transcript keeps the rows.

const (
	planCapNormal  = 5 // tally + up to four steps
	planCapCompact = 1
)

func (m *model) planTodos() []agent.Todo {
	if m.opts.Agent == nil {
		return nil
	}
	return m.opts.Agent.Todos()
}

func (m *model) planHeight() int {
	if m.picker != nil || m.connect != nil || m.ov.kind != "" || m.asking != nil {
		return 0
	}
	return len(m.planLines())
}

func (m *model) planLines() []string {
	todos := m.planTodos()
	if len(todos) == 0 || m.tight() {
		return nil
	}
	w := m.width - m.gutter()
	if w < 20 {
		w = 20
	}
	done, doing := 0, ""
	for _, t := range todos {
		if t.Done() {
			done++
		}
		if t.Status == agent.TodoDoing {
			doing = t.Text
		}
	}
	tally := fmt.Sprintf("plan  %d of %d done", done, len(todos))
	if m.compactMode() {
		line := tally
		if doing != "" {
			line += "  ·  [>] " + doing
		}
		return []string{truncateLine(line, w)}
	}
	lines := []string{m.styles.key.Render(tally)}
	shown := selectPlanSteps(todos, planCapNormal-1)
	for _, t := range shown {
		mark, st := "[ ]", m.styles.dim
		switch t.Status {
		case agent.TodoDone:
			mark, st = "[x]", m.styles.ok
		case agent.TodoDoing:
			mark, st = "[>]", m.styles.warn
		}
		line := st.Render(mark) + " " + t.Text
		if len(t.BlockedBy) > 0 && !t.Done() {
			line += m.styles.faint.Render("  after " + strings.Join(t.BlockedBy, "; "))
		}
		lines = append(lines, truncateLine(line, w))
	}
	if extra := len(todos) - len(shown); extra > 0 {
		lines[0] = m.styles.key.Render(tally) + m.styles.faint.Render(fmt.Sprintf("  ·  %d more in /todos", extra))
	}
	return lines
}

// selectPlanSteps keeps the in-progress step, the last completed one,
// and the next pending ones, so a long plan still shows where it is.
func selectPlanSteps(todos []agent.Todo, cap int) []agent.Todo {
	if len(todos) <= cap {
		return todos
	}
	var doing, pending, done []agent.Todo
	for _, t := range todos {
		switch t.Status {
		case agent.TodoDoing:
			doing = append(doing, t)
		case agent.TodoDone:
			done = append(done, t)
		default:
			pending = append(pending, t)
		}
	}
	out := doing
	if len(done) > 0 && len(out) < cap {
		out = append([]agent.Todo{done[len(done)-1]}, out...)
	}
	for _, t := range pending {
		if len(out) >= cap {
			break
		}
		out = append(out, t)
	}
	if len(out) < cap {
		for i := len(done) - 2; i >= 0 && len(out) < cap; i-- {
			out = append([]agent.Todo{done[i]}, out...)
		}
	}
	return out
}

func (m *model) planView() string {
	lines := m.planLines()
	if len(lines) == 0 {
		return ""
	}
	out := strings.Join(lines, "\n")
	if g := m.gutter(); g > 0 {
		out = indent(out, strings.Repeat(" ", g))
	}
	return out
}

// planCommand enters plan mode, approves the plan, or leaves the mode.
func (m *model) planCommand(arg string) tea.Cmd {
	ag := m.opts.Agent
	switch arg {
	case "approve", "go", "execute":
		if !ag.Settings().PlanMode {
			m.transcript.errorf("not in plan mode: /plan <request> writes a plan first")
			m.refresh(false)
			return nil
		}
		if m.turn.running {
			m.transcript.errorf("wait for the plan to finish")
			m.refresh(false)
			return nil
		}
		ag.SetPlanMode(false)
		m.status = "plan approved · executing · /undo reverts a step"
		return m.submit(agent.PlanApprovedPrompt)
	case "off", "cancel", "exit":
		ag.SetPlanMode(false)
		m.noteMode()
		m.refresh(false)
		return nil
	}
	if !ag.Settings().PlanMode {
		ag.SetPlanMode(true)
		m.noteMode()
	}
	if arg == "" {
		m.refresh(false)
		return nil
	}
	return m.submit(arg)
}
