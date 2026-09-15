package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)

// effortCommand implements /effort [tier|up|down] through the agent's
// core command (see agent.EffortCommand): this front-end only parses the
// line and renders what came back.
func (m *model) effortCommand(arg string) {
	if m.opts.Agent == nil {
		m.transcript.errorf("no agent")
		return
	}
	m.showResult(m.opts.Agent.EffortCommand(arg))
}

// keyCycleEffort is ctrl+r: step the reasoning tier up the ladder, and
// round to the provider's default at the top. It is the counterpart of
// shift+tab for modes — the one setting you change between one step of a
// task and the next, and the one the status line already shows.
func (m *model) keyCycleEffort(tea.KeyMsg) (tea.Cmd, bool) {
	if m.opts.Agent == nil {
		return nil, true
	}
	next, ok := llm.StepEffort(m.opts.Agent.Settings().Effort, 1)
	if !ok {
		next = "" // wrap from the top rung back to the provider's default
	}
	m.showResult(m.opts.Agent.EffortCommand(effortArg(next)))
	m.refresh(false)
	return nil, true
}

// effortArg spells a tier as /effort would take it, including the
// provider's own default.
func effortArg(tier string) string {
	if tier == "" {
		return "default"
	}
	return tier
}
