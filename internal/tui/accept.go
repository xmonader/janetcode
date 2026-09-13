package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

// acceptCommand runs /accept: starts test-writing mode, reports status,
// or resolves approve/lock/unlock/off. agent.ParseAcceptArgs is the one
// parser both front-ends use, so a spelling accepted here means the same
// thing in the web UI.
func (m *model) acceptCommand(arg string) tea.Cmd {
	ag := m.opts.Agent
	a := agent.ParseAcceptArgs(arg)
	switch {
	case a.Status:
		m.transcript.info(ag.AcceptStatus())
	case a.Approve:
		cmd, err := ag.AcceptApprove()
		if err != nil {
			m.transcript.errorf("accept: %v", err)
			break
		}
		m.note("accept: locked · verifies with: " + cmd)
	case a.Lock:
		if err := ag.AcceptLock(); err != nil {
			m.transcript.errorf("accept: %v", err)
			break
		}
		m.note("accept: locked")
	case a.Unlock:
		if err := ag.AcceptUnlock(); err != nil {
			m.transcript.errorf("accept: %v", err)
			break
		}
		m.note("accept: unlocked · edits to the test files are allowed until /accept lock")
	case a.Off:
		ag.AcceptOff()
		m.note("accept: off")
	default:
		ag.AcceptStart(a.Request, m.opts.Cwd)
		m.status = "accept: writing tests for " + a.Request
		m.refresh(false)
		return m.submit(a.Request)
	}
	m.refresh(false)
	return nil
}
