package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// recapMsg carries a finished /recap. It goes to the scrollback as an
// info line and nowhere else: the model never sees it.
type recapMsg struct {
	text string
	err  error
	auto bool
}

// recapCommand starts a recap in the background.
func (m *model) recapCommand(auto bool) tea.Cmd {
	ag := m.opts.Agent
	if ag == nil {
		if !auto {
			m.transcript.errorf("no agent")
			m.refresh(false)
		}
		return nil
	}
	m.recapBusy = true
	send, life := m.send, m.life
	go func() {
		ctx, cancel := context.WithTimeout(life, 45*time.Second)
		defer cancel()
		a, err := ag.Recap(ctx)
		send(recapMsg{text: a, err: err, auto: auto})
	}()
	if !auto {
		m.note("recap…")
		m.refresh(false)
	}
	return nil
}

func (m *model) handleRecap(msg recapMsg) {
	m.recapBusy = false
	switch {
	case msg.err != nil && msg.auto:
		// A recap nobody asked for stays quiet when it fails.
	case msg.err != nil:
		m.transcript.errorf("recap: %v", msg.err)
	default:
		m.transcript.info("recap\n" + msg.text)
	}
	m.refresh(false)
}

// autoRecap fires a recap on the first keypress after recap_idle_minutes
// without one. It tracks the last key, not the last turn: a long turn the
// user watched is not a break. It is a no-op while a turn runs or a
// recap is in flight, and when nothing was said since the last one.
func (m *model) autoRecap(now time.Time) tea.Cmd {
	last := m.lastKey
	m.lastKey = now
	if m.opts.Agent == nil || m.opts.Config == nil || m.opts.Config.RecapIdleMinutes <= 0 || last.IsZero() {
		return nil
	}
	if now.Sub(last) < time.Duration(m.opts.Config.RecapIdleMinutes)*time.Minute {
		return nil
	}
	if m.recapBusy || m.turn.running {
		return nil
	}
	if n := len(m.opts.Agent.MessagesSnapshot()); n == 0 || n == m.recapAtMsgs {
		return nil
	}
	m.recapAtMsgs = len(m.opts.Agent.MessagesSnapshot())
	return m.recapCommand(true)
}
