package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

// acceptCommand runs /accept through agent.AcceptCommand: starts
// test-writing mode (submitting the request as a turn), reports status,
// or resolves approve/lock/unlock/command/off. agent.ParseAcceptArgs is
// the one parser both front-ends use, so a spelling accepted here means
// the same thing in the web UI.
func (m *model) acceptCommand(arg string) tea.Cmd {
	res, err := m.opts.Agent.AcceptCommand(agent.ParseAcceptArgs(arg), m.opts.Cwd)
	m.showResult(res, err)
	m.refresh(false)
	if err == nil && res.Handover != "" {
		return m.submit(res.Handover)
	}
	return nil
}
