package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

// secretMsg is the model asking for a secret, delivered from the agent
// goroutine into the UI loop.
type secretMsg struct {
	ctx   context.Context
	req   agent.SecretRequest
	reply chan agent.SecretReply
}

// secretState is the private prompt. Its field is its own masked
// textinput, not the composer: what is typed here must never reach the
// composer's value, the prompt history, a draft or a frame of the
// transcript.
type secretState struct {
	req   agent.SecretRequest
	field textinput.Model
	reply chan agent.SecretReply
	// draft is what the composer held when the prompt came up.
	draft string
}

// AskSecret implements agent.SecretAsker. One prompt is up at a time: a
// second call waits its turn rather than stacking masked fields.
func (a *tuiAsker) AskSecret(ctx context.Context, req agent.SecretRequest) (agent.SecretReply, error) {
	if a == nil || a.send == nil {
		return agent.SecretReply{}, fmt.Errorf("ask: no UI")
	}
	select {
	case a.secretSlot() <- struct{}{}:
		defer func() { <-a.secretSlot() }()
	case <-ctx.Done():
		return agent.SecretReply{}, ctx.Err()
	}
	reply := make(chan agent.SecretReply, 1)
	a.send(secretMsg{ctx: ctx, req: req, reply: reply})
	select {
	case v := <-reply:
		return v, nil
	case <-ctx.Done():
		return agent.SecretReply{}, ctx.Err()
	}
}

func (a *tuiAsker) secretSlot() chan struct{} {
	a.slotOnce.Do(func() { a.slot = make(chan struct{}, 1) })
	return a.slot
}

func (m *model) openSecret(msg secretMsg) {
	f := textinput.New()
	f.Prompt = ""
	f.EchoMode = textinput.EchoPassword
	f.EchoCharacter = '•'
	f.CharLimit = 4096
	f.Focus()
	m.secreting = &secretState{req: msg.req, field: f, reply: msg.reply, draft: m.input.Value()}
	m.setInput("")
	m.input.Blur()
	m.turn.setPhase("waiting for your secret")
	m.refresh(false)
}

// finishSecret closes the prompt. A zero value is a decline. The field is
// blanked before it is dropped so nothing keeps the typed text alive.
func (m *model) finishSecret(value string, declined bool) {
	s := m.secreting
	if s == nil {
		return
	}
	s.field.SetValue("")
	m.secreting = nil
	m.setInput(s.draft)
	m.input.Focus()
	m.turn.setPhase("")
	select {
	case s.reply <- agent.SecretReply{Value: value, Declined: declined}:
	default:
	}
	m.refresh(false)
}

func (m *model) handleSecretKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	s := m.secreting
	if s == nil {
		return nil, false
	}
	if !msg.Paste {
		switch keyName(msg) {
		case "enter":
			v := strings.TrimRight(s.field.Value(), "\r\n")
			if v == "" {
				m.finishSecret("", true)
				m.note("secret request declined")
				return nil, true
			}
			m.finishSecret(v, false)
			m.note("secret " + s.req.EnvVar + " saved for this session")
			return nil, true
		case "esc", "ctrl+c":
			m.finishSecret("", true)
			m.note("secret request declined")
			return nil, true
		}
	}
	var cmd tea.Cmd
	s.field, cmd = s.field.Update(msg)
	m.refresh(false)
	return cmd, true
}

func (m *model) secretView(height int) string {
	s := m.secreting
	w := modalBoxWidth(m.width)
	inner := w - 4
	var lines []string
	add := func(t string) { lines = append(lines, truncateLine(t, inner)) }
	// Everything but the masked field is the model's text.
	add(m.styles.key.Render("Secret requested: " + sanitize(s.req.Name)))
	add("")
	add(m.styles.dim.Render("Why: ") + sanitize(s.req.Purpose))
	add(m.styles.dim.Render("Use: ") + sanitize(s.req.Usage))
	add(m.styles.dim.Render("It will be available to commands as $" + s.req.EnvVar + " for this session only."))
	add(m.styles.dim.Render("The model never sees the value, and it is not written to the session."))
	add("")
	add(m.styles.accent.Render("› ") + s.field.View())
	add("")
	add(m.styles.faint.Render("enter saves · esc or an empty enter declines"))
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return m.askPlace(box, height)
}
