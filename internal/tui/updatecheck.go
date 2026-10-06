package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// updateNoticeMsg carries the release notice back from its goroutine.
type updateNoticeMsg string

// checkUpdate asks, once and in the background, whether a newer release
// is out; a slow or absent network delays nothing.
func (m *model) checkUpdate() tea.Cmd {
	ask := m.opts.UpdateNotice
	if ask == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return updateNoticeMsg(ask(ctx))
	}
}
