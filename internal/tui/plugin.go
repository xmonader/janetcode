package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/plugin/plugincmd"
)

// pluginCommand answers /plugin. What fetches over the network runs off
// the event loop and reports through the same message /mcp's commands
// use; the rest answers at once.
func (m *model) pluginCommand(args []string) tea.Cmd {
	if m.opts.ConfigDir == "" {
		m.transcript.errorf("plugins need a config directory, and this session has none")
		m.refresh(false)
		return nil
	}
	dir := m.opts.ConfigDir
	if !plugincmd.Slow(args) {
		res, err := plugincmd.Run(m.life, dir, args)
		m.showResult(res, err)
		m.refresh(false)
		return nil
	}
	m.status = "fetching…"
	m.refresh(false)
	send, life := m.send, m.life
	go func() {
		res, err := plugincmd.Run(life, dir, args)
		send(mcpCmdMsg{res: res, err: err})
	}()
	return m.spinner.Tick
}
