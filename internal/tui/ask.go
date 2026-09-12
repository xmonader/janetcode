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
}

type tuiAsker struct{ send func(tea.Msg) }

func (a *tuiAsker) Ask(ctx context.Context, qs []agent.Question) ([]agent.Answer, error) {
	if a == nil || a.send == nil {
		return nil, fmt.Errorf("ask: no UI")
	}
	reply := make(chan []agent.Answer, 1)
	a.send(askMsg{questions: qs, reply: reply})
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
	m.asking = &askState{
		questions: qs,
		answers:   make([]agent.Answer, len(qs)),
		reply:     msg.reply,
	}
	m.input.Blur()
	m.setPhase("waiting for your answer")
	m.refresh(false)
}

func (m *model) finishAsk() {
	s := m.asking
	if s == nil {
		return
	}
	m.asking = nil
	m.input.Reset()
	m.input.Focus()
	m.setPhase("")
	select {
	case s.reply <- s.answers:
	default:
	}
	m.refresh(false)
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
	add(styleKey.Render("Question"))
	add(m.askTabStrip(inner))
	add("")
	q := s.questions[s.tab]
	add(styleKey.Render(q.Prompt))
	if q.Detail != "" {
		add(styleDim.Render(q.Detail))
	}
	add("")
	opts := s.options()
	for i, o := range opts {
		mark := "  "
		if i == s.opt {
			mark = styleAccent.Render("▸ ")
		}
		picked := ""
		if s.answers[s.tab].Label == o.Label {
			picked = styleOK.Render(" ✓")
		}
		desc := ""
		if o.Description != "" {
			desc = "  " + styleFaint.Render(o.Description)
		}
		add(mark + fmt.Sprintf("%d. ", i+1) + o.Label + picked + desc)
	}
	if s.othering {
		add("")
		add(styleAccent.Render("› ") + m.input.Value())
		add(styleDim.Render("type your answer · enter sends · esc back to the list"))
	} else {
		add("")
		add(styleFaint.Render("tab / ←→ switch questions · ↑↓ choose · 1-9 or enter picks · esc skip remaining"))
	}
	box := styleDialog.Width(w - 2).Render(strings.Join(lines, "\n"))
	return clipHeight(lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, box), height)
}

func (m *model) askTabStrip(width int) string {
	s := m.asking
	var parts []string
	for i, q := range s.questions {
		label := fmt.Sprintf("%d %s", i+1, q.Prompt)
		if a := s.answers[i].Label; a != "" {
			label += " ✓"
		}
		st := styleDim
		if i == s.tab {
			st = styleKey
		}
		parts = append(parts, st.Render(label))
	}
	return truncateLine(strings.Join(parts, "  ·  "), width)
}
