package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
)

// A question with a recommendation opens with the cursor on it, shows the
// countdown, and after the turn went on without the answer says that an
// answer now still reaches the model.
func TestAskShowsRecommendationCountdownAndLateMode(t *testing.T) {
	h := newHarness(t)
	reply := make(chan []agent.Answer, 1)
	qs := []agent.Question{{
		Prompt:      "Which port?",
		Options:     []agent.AskOption{{Label: "8080"}, {Label: "9000"}},
		Recommended: "9000",
		Deadline:    time.Now().Add(90 * time.Second),
	}}
	h.update(askMsg{ctx: context.Background(), questions: qs, reply: reply})
	if h.m.asking == nil || h.m.asking.opt != 1 {
		t.Fatalf("the cursor is not on the recommendation: %+v", h.m.asking)
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "9000 (recommended)") || !strings.Contains(v, "proceeds with 9000") {
		t.Fatalf("no recommendation or countdown:\n%s", v)
	}
	h.update(askTimedOutMsg{})
	v = plain(h.m.View())
	if !strings.Contains(v, "went on with 9000") || !strings.Contains(v, "late answer") {
		t.Fatalf("the question does not say the turn went on:\n%s", v)
	}
	h.enter() // takes the recommendation the cursor is on
	if a := <-reply; len(a) != 1 || a[0].Label != "9000" {
		t.Fatalf("answered %+v", a)
	}
}

// A goal round or a /loop firing is sent by the harness, not typed, so a
// question in it is held to the unattended wait.
func TestHarnessPromptMarksTheTurnUnattended(t *testing.T) {
	h := newHarness(t)
	if _, alone := h.m.opts.Agent.AskWaitFor(); alone {
		t.Fatal("a fresh session counts as unattended")
	}
	h.m.submitAs("continue the goal", "◉ goal round 2")
	if _, alone := h.m.opts.Agent.AskWaitFor(); !alone {
		t.Fatal("a goal round was not marked unattended")
	}
}
