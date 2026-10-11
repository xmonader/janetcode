package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

// TestLateQuestionOutlivesTheTurn: a question whose wait ran out says
// "answer now and the model gets it as a late answer", and the agent
// keeps listening past the end of the turn (the answer goes with the next
// message). But the end of the turn took the question down with blank
// answers, usually seconds after it said so, and the answer could never
// be given.
func TestLateQuestionOutlivesTheTurn(t *testing.T) {
	h := newHarness(t)
	h.m.startTurn("thinking")
	reply := make(chan []agent.Answer, 1)
	qs := []agent.Question{{
		Prompt:      "Which port?",
		Options:     []agent.AskOption{{Label: "8080"}, {Label: "9000"}},
		Recommended: "9000",
		Deadline:    time.Now().Add(time.Second),
	}}
	h.update(askMsg{ctx: context.Background(), questions: qs, reply: reply})
	h.update(askTimedOutMsg{})
	h.m.finishTurn(agentDoneMsg{})
	if h.m.asking == nil {
		t.Fatal("the end of the turn took down a question that is still taking a late answer")
	}
	h.update(tea.KeyMsg{Type: tea.KeyUp}) // 8080, not the recommendation
	h.enter()
	select {
	case a := <-reply:
		if len(a) != 1 || a[0].Label != "8080" {
			t.Fatalf("late answer %+v, want 8080", a)
		}
	default:
		t.Fatal("no late answer reached the asker")
	}

	// A question the turn was still waiting on goes with the turn.
	h.m.startTurn("thinking")
	h.update(askMsg{ctx: context.Background(), questions: qs, reply: make(chan []agent.Answer, 1)})
	h.m.finishTurn(agentDoneMsg{err: context.Canceled})
	if h.m.asking != nil {
		t.Fatal("a question of a cancelled turn stayed up")
	}
}
