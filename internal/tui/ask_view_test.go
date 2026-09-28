package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
)

// A question is about what was just said, so the transcript has to stay on
// screen. A recorded plan-mode session asked "Ready to implement?" on a
// cleared screen with the plan it was about gone, and printed the prompt
// twice — once as a one-tab strip, once as the prompt.
func TestAskKeepsTheTranscriptAndAsksOnce(t *testing.T) {
	h := newHarness(t)
	const plan = "PLAN LINE: write the parser, then the tests"
	h.m.transcript.banner(plan)
	for i := 0; i < 20; i++ {
		h.m.transcript.info("filler line so the transcript is long")
	}
	h.m.transcript.banner(plan)

	qs := []agent.Question{{
		Prompt:  "Ready to implement?",
		Options: []agent.AskOption{{Label: "Go ahead"}, {Label: "Tweak something"}},
	}}
	go h.m.opts.Agent.Asker.Ask(context.Background(), qs)
	h.pump(func() bool { return h.m.asking != nil })

	v := plain(h.m.View())
	if n := strings.Count(v, "Ready to implement?"); n != 1 {
		t.Fatalf("the prompt appears %d times, want 1:\n%s", n, v)
	}
	if !strings.Contains(v, plan) {
		t.Fatalf("the question hid what it is about:\n%s", v)
	}
	if !strings.Contains(v, "Go ahead") {
		t.Fatalf("no options:\n%s", v)
	}
}

// TestTwoAsksAtOnceAreBothAnswered: the ask tool is not mutating, so two
// ask calls in one reply run side by side. The second replaced the first
// on screen and the first call waited for ever, holding the turn.
func TestTwoAsksAtOnceAreBothAnswered(t *testing.T) {
	h := newHarness(t)
	q := func(p string) []agent.Question {
		return []agent.Question{{Prompt: p, Options: []agent.AskOption{{Label: "Yes"}, {Label: "No"}}}}
	}
	first, second := make(chan []agent.Answer, 1), make(chan []agent.Answer, 1)
	h.update(askMsg{ctx: context.Background(), questions: q("First?"), reply: first})
	h.update(askMsg{ctx: context.Background(), questions: q("Second?"), reply: second})
	h.keys("1")
	select {
	case a := <-first:
		if len(a) != 1 || a[0].Label != "Yes" {
			t.Fatalf("first answered %+v", a)
		}
	default:
		t.Fatal("the first question was never answered: the second replaced it")
	}
	if h.m.asking == nil || h.m.asking.questions[0].Prompt != "Second?" {
		t.Fatal("the second question is not up after the first was answered")
	}
	h.keys("2")
	if a := <-second; len(a) != 1 || a[0].Label != "No" {
		t.Fatalf("second answered %+v", a)
	}
}
