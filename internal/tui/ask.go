package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
)

// askMsg is a batch of questions the model wants answered, delivered
// from the agent goroutine into the UI loop.
type askMsg struct {
	// ctx is the asking caller's: once it is done nobody reads reply.
	ctx       context.Context
	questions []agent.Question
	reply     chan []agent.Answer
}

// askState is the tabbed question prompt. Several questions share one
// overlay; left/right (or tab) switches tabs without dismissing it.
type askState struct {
	questions []agent.Question
	answers   []agent.Answer
	tab       int
	opt       int
	othering  bool
	reply     chan []agent.Answer
	// draft is what was in the composer when the questions came up. The
	// "Other" answer is typed in the composer and finishing clears it,
	// so the message the user was writing is put back afterwards.
	draft string
}

type tuiAsker struct{ send func(tea.Msg) }

func (a *tuiAsker) Ask(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	if a == nil || a.send == nil {
		return nil, fmt.Errorf("ask: no UI")
	}
	reply := make(chan []agent.Answer, 1)
	a.send(askMsg{ctx: ctx, questions: qs, reply: reply})
	select {
	case v := <-reply:
		return v, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *askState) options() []agent.AskOption {
	q := s.questions[s.tab]
	out := append([]agent.AskOption(nil), q.Options...)
	out = append(out, agent.AskOption{Label: agent.OtherLabel, Description: "type your own answer"})
	return out
}

func (s *askState) allAnswered() bool {
	for _, a := range s.answers {
		if strings.TrimSpace(a.Label) == "" {
			return false
		}
	}
	return true
}

func (m *model) openAsk(msg askMsg) {
	qs := msg.questions
	if len(qs) == 0 {
		msg.reply <- nil
		return
	}
	if m.asking != nil {
		// The ask tool is not mutating, so two calls in one reply run side
		// by side. The second used to replace the first on screen, and the
		// first call waited for an answer that could no longer be given,
		// holding the whole turn with it. It waits its turn instead.
		m.waitingAsks = append(m.waitingAsks, msg)
		return
	}
	m.asking = &askState{
		questions: qs,
		answers:   make([]agent.Answer, len(qs)),
		reply:     msg.reply,
		draft:     m.input.Value(),
	}
	m.setInput("")
	m.input.Blur()
	m.turn.setPhase("waiting for your answer")
	m.refresh(false)
}

func (m *model) finishAsk() {
	s := m.asking
	if s == nil {
		return
	}
	m.asking = nil
	m.input.Reset()
	m.setInput(s.draft)
	m.input.Focus()
	m.turn.setPhase("")
	select {
	case s.reply <- s.answers:
	default:
	}
	m.refresh(false)
	// The next batch of questions that was waiting behind this one, if
	// its caller is still there to read the answers.
	for m.asking == nil && len(m.waitingAsks) > 0 {
		next := m.waitingAsks[0]
		m.waitingAsks = m.waitingAsks[1:]
		if next.ctx != nil && next.ctx.Err() != nil {
			continue
		}
		m.openAsk(next)
	}
}

func (m *model) handleAskKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	s := m.asking
	if s == nil {
		return nil, false
	}
	if s.othering {
		switch keyName(msg) {
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			s.answers[s.tab] = agent.Answer{Label: agent.OtherLabel, Reason: text}
			s.othering = false
			m.input.Reset()
			m.input.Blur()
			if s.allAnswered() {
				m.finishAsk()
				return nil, true
			}
			m.nextUnansweredTab()
			m.refresh(false)
			return nil, true
		case "esc":
			s.othering = false
			m.input.Reset()
			m.input.Blur()
			m.refresh(false)
			return nil, true
		}
		return nil, false // textarea
	}
	opts := s.options()
	switch keyName(msg) {
	case "esc", "ctrl+c":
		for i := range s.answers {
			if s.answers[i].Label == "" {
				s.answers[i] = agent.Answer{Label: "skipped"}
			}
		}
		m.finishAsk()
	case "left", "shift+tab":
		s.tab = (s.tab - 1 + len(s.questions)) % len(s.questions)
		s.opt = 0
		m.refresh(false)
	case "right", "tab":
		s.tab = (s.tab + 1) % len(s.questions)
		s.opt = 0
		m.refresh(false)
	case "up", "ctrl+p":
		if len(opts) > 0 {
			s.opt = (s.opt - 1 + len(opts)) % len(opts)
			m.refresh(false)
		}
	case "down", "ctrl+n":
		if len(opts) > 0 {
			s.opt = (s.opt + 1) % len(opts)
			m.refresh(false)
		}
	case "enter":
		m.pickAskOption(s.opt)
	default:
		if n := keyName(msg); len(n) == 1 && n[0] >= '1' && n[0] <= '9' {
			i := int(n[0] - '1')
			if i < len(opts) {
				m.pickAskOption(i)
			}
		}
	}
	return nil, true
}

func (m *model) pickAskOption(i int) {
	s := m.asking
	opts := s.options()
	if i < 0 || i >= len(opts) {
		return
	}
	opt := opts[i]
	if opt.Label == agent.OtherLabel {
		s.othering = true
		m.input.Reset()
		m.input.Focus()
		m.layout()
		m.refresh(false)
		return
	}
	s.answers[s.tab] = agent.Answer{Label: opt.Label}
	if s.allAnswered() {
		m.finishAsk()
		return
	}
	m.nextUnansweredTab()
	m.refresh(false)
}

func (m *model) nextUnansweredTab() {
	s := m.asking
	for i := 1; i <= len(s.questions); i++ {
		j := (s.tab + i) % len(s.questions)
		if s.answers[j].Label == "" {
			s.tab, s.opt = j, 0
			return
		}
	}
}

func (m *model) askView(height int) string {
	s := m.asking
	w := min(dialogWidth, m.width-4)
	inner := w - 4
	var lines []string
	add := func(t string) { lines = append(lines, truncateLine(t, inner)) }
	q := s.questions[s.tab]
	// The strip is how you switch between questions, and with one there is
	// nothing to switch to: it printed "1 Ready to implement?" directly
	// above "Ready to implement?".
	if len(s.questions) > 1 {
		add(m.styles.key.Render("Question"))
		add(m.askTabStrip(inner))
		add("")
	}
	// The questions and their options are the model's text; like the
	// plan's steps they may not carry control characters to the terminal.
	add(m.styles.key.Render(sanitize(q.Prompt)))
	if q.Detail != "" {
		add(m.styles.dim.Render(sanitize(q.Detail)))
	}
	add("")
	opts := s.options()
	for i, o := range opts {
		mark := "  "
		if i == s.opt {
			mark = m.styles.accent.Render("▸ ")
		}
		picked := ""
		if s.answers[s.tab].Label == o.Label {
			picked = m.styles.ok.Render(" ✓")
		}
		desc := ""
		if o.Description != "" {
			desc = "  " + m.styles.faint.Render(sanitize(o.Description))
		}
		add(mark + fmt.Sprintf("%d. ", i+1) + sanitize(o.Label) + picked + desc)
	}
	if s.othering {
		add("")
		add(m.styles.accent.Render("› ") + m.input.Value())
		add(m.styles.dim.Render("type your answer · enter sends · esc back to the list"))
	} else {
		add("")
		add(m.styles.faint.Render("tab / ←→ switch questions · ↑↓ choose · 1-9 or enter picks · esc skip remaining"))
	}
	box := m.styles.dialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return m.askPlace(box, height)
}

// askPlace puts the question box on screen with the transcript still
// behind it. A question is about what was just said — "Ready to
// implement?" under a plan — and the dialog used to be centred on a
// cleared screen, so the plan being approved was not on it: there was
// nothing left to judge. The bottom of the transcript stays, the box sits
// under it, and a window too short for both falls back to the box alone.
func (m *model) askPlace(box string, height int) string {
	centred := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, box)
	room := height - lipgloss.Height(box) - 1
	const leastContext = 3
	if room < leastContext {
		return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
	}
	return clipHeight(lipgloss.JoinVertical(lipgloss.Left, lastLines(m.viewport.View(), room), "", centred), height)
}

// lastLines is exactly the last n lines of s, which for the transcript
// viewport is what is nearest the question. (tailLines in toolview.go
// adds an elision note and some slack; here the box's room is exact.)
func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (m *model) askTabStrip(width int) string {
	s := m.asking
	var parts []string
	for i, q := range s.questions {
		label := fmt.Sprintf("%d %s", i+1, sanitize(q.Prompt))
		if a := s.answers[i].Label; a != "" {
			label += " ✓"
		}
		st := m.styles.dim
		if i == s.tab {
			st = m.styles.key
		}
		parts = append(parts, st.Render(label))
	}
	return truncateLine(strings.Join(parts, "  ·  "), width)
}
