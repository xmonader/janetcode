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
