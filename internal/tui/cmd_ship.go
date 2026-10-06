package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/cmdresult"
)

// shipMsg carries the result of /commit or /pr back from its goroutine.
type shipMsg struct {
	res cmdresult.Result
	err error
}

// shipCommand runs /commit or /pr off the UI goroutine: each drafts its
// words with a model call, and /pr pushes over the network.
func (m *model) shipCommand(what string, run func(context.Context, string, string) (cmdresult.Result, error), arg string) tea.Cmd {
	ctx := m.startSideWork(what)
	cwd, send := m.opts.Cwd, m.send
	go func() {
		res, err := run(ctx, cwd, arg)
		send(shipMsg{res: res, err: err})
	}()
	m.refresh(false)
	return m.spinner.Tick
}
