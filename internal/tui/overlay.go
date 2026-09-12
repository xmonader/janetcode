package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
)

// overlayAgents is the live subagent panel. Unlike the picker it is not
// a snapshot: each View re-reads Task.Children().
const overlayAgents = "agents"

func (m *model) taskTool() *agent.Task {
	ag := m.opts.Agent
	if ag == nil || ag.Tools == nil {
		return nil
	}
	t, ok := ag.Tools.Get("task")
	if !ok {
		return nil
	}
	task, ok := t.(*agent.Task)
	if !ok {
		return nil
	}
	return task
}

func (m *model) openOverlay(kind string) {
	m.overlay = kind
	m.input.Blur()
}

func (m *model) closeOverlay() {
	m.overlay = ""
	m.input.Focus()
	m.refresh(false)
}

func (m *model) handleOverlayKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch keyName(msg) {
	case "esc", "ctrl+c":
		m.closeOverlay()
	}
	return nil, true
}

func (m *model) overlayView(height int) string {
	switch m.overlay {
	case overlayAgents:
		return m.agentsOverlayView(height)
	}
	return ""
}

func (m *model) agentsOverlayView(height int) string {
	w := min(dialogWidth, m.width-4)
	inner := w - 4
	var lines []string
	add := func(s string) { lines = append(lines, truncateLine(s, inner)) }
	add(styleKey.Render("Subagents"))
	add(styleDim.Render("this session · live · esc closes"))
	add("")
	var kids []agent.ChildInfo
	if t := m.taskTool(); t != nil {
		kids = t.Children()
	}
	if len(kids) == 0 {
		add("  " + styleDim.Render("no subagents"))
	} else {
		for _, k := range kids {
			state, stateStyle := "done", styleOK
			if !k.Done {
				state, stateStyle = "running", styleWarn
			}
			age := shortDuration(time.Since(k.Started))
			add(fmt.Sprintf("%s  %s  %s  %s",
				styleKey.Render(k.ID),
				stateStyle.Render(fmt.Sprintf("%-7s", state)),
				styleFaint.Render(age),
				k.Description))
			if k.Last != "" {
				label := "last"
				if !k.Done {
					label = "now"
				}
				add("    " + styleDim.Render(label+": "+k.Last))
			}
			if k.Usage.InputTokens+k.Usage.OutputTokens > 0 {
				add("    " + styleFaint.Render(fmt.Sprintf("tokens in=%s out=%s",
					shortTokens(k.Usage.InputTokens), shortTokens(k.Usage.OutputTokens))))
			}
			add("")
		}
	}
	add(styleFaint.Render(`esc close · steer one by asking: "tell task-2 to also check …"`))
	box := styleDialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}
