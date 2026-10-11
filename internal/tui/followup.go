package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type followupMsg struct {
	gen  int
	text string
}

func (m *model) followupCommand(args []string) {
	switch {
	case len(args) == 0:
		if m.followupOn {
			m.note("follow-up suggestions on · tab accepts · /followup off silences them")
		} else {
			m.note("follow-up suggestions are off · /followup on brings them back")
		}
	case args[0] == "on":
		m.followupOn = true
		m.note("follow-up suggestions on · tab accepts after a turn")
	case args[0] == "off":
		m.followupOn = false
		m.followup = ""
		m.followupGen++
		m.note("follow-up suggestions off · /followup on brings them back")
	default:
		m.transcript.errorf("usage: /followup [on|off]")
	}
}

func (m *model) suggestFollowup() tea.Cmd {
	if !m.followupOn || m.opts.Agent == nil {
		return nil
	}
	m.followup = ""
	m.followupGen++
	gen := m.followupGen
	ag, send, life := m.opts.Agent, m.send, m.life
	go func() {
		ctx, cancel := context.WithTimeout(life, 15*time.Second)
		defer cancel()
		text, err := ag.SuggestFollowup(ctx)
		if err != nil || text == "" {
			return
		}
		send(followupMsg{gen: gen, text: text})
	}()
	return nil
}

func (m *model) acceptFollowup() bool {
	if m.followup == "" || strings.TrimSpace(m.input.Value()) != "" {
		return false
	}
	m.input.SetValue(m.followup)
	m.input.CursorEnd()
	m.followup = ""
	m.followupGen++
	return true
}
