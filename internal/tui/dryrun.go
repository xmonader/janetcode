package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// cmdDryrun runs "/dryrun [on|off|show|apply [n,m…]|discard]". Apply
// takes a checkpoint (git, file copies) before it writes, so it runs off
// the UI goroutine the way /undo apply does; the rest only flips state.
func (m *model) cmdDryrun(text string, fields []string) (tea.Cmd, bool) {
	arg := cmdArg(text, fields)
	ag := m.opts.Agent
	if first, _, _ := strings.Cut(strings.TrimSpace(arg), " "); strings.EqualFold(first, "apply") {
		ctx := m.startSideWork("applying the dry-run preview")
		send := m.send
		go func() {
			res, err := ag.DryRunCommand(ctx, arg)
			send(undoAboutMsg{res: res, err: err})
		}()
		m.refresh(false)
		return m.spinner.Tick, false
	}
	res, err := ag.DryRunCommand(m.life, arg)
	m.showResult(res, err)
	return nil, false
}
