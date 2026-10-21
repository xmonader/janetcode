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
	first, _, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if m.turn.running && !dryrunReadOnly(first) {
		// Apply runs as side work, which would start over the running
		// turn and, ending, mark it idle while it still runs; off and
		// discard write a note into the history the turn is writing.
		m.transcript.errorf("%s", m.waitsForTurn(strings.TrimSpace("/dryrun "+strings.ToLower(first))))
		m.refresh(false)
		return nil, false
	}
	if strings.EqualFold(first, "apply") {
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

// dryrunReadOnly reports whether a /dryrun form only reads the preview,
// so it may run while a turn does.
func dryrunReadOnly(sub string) bool {
	switch strings.ToLower(sub) {
	case "status", "list", "preview", "show", "diff":
		return true
	}
	return false
}
