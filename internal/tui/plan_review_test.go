package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/plan"
)

// The plan review loop from the terminal: a plan-mode turn leaves a file
// behind, /plan review shows it numbered, /plan comment marks lines, and
// /plan changes sends them all at once without leaving plan mode.
func TestPlanReviewCommentsAndChanges(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Looked around.\n\n## Plan\n1. rename Parser\n2. update the tests"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "## Plan\n1. rename Parser to Parse\n2. update the tests"), StopReason: llm.StopEndTurn},
	)
	h.keys("/plan rename the parser")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	root := h.m.opts.Cwd
	if _, err := os.Stat(plan.File(root)); err != nil {
		t.Fatalf("a plan-mode turn left no plan file: %v", err)
	}

	h.keys("/plan review")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"1 │ ## Plan", "2 │ 1. rename Parser", "3 │ 2. update the tests"} {
		if !strings.Contains(v, want) {
			t.Fatalf("/plan review is missing %q, view:\n%s", want, v)
		}
	}

	h.keys("/plan comment 2 rename it to what?")
	h.enter()
	h.keys("/plan comment plan.md:3 which tests?")
	h.enter()
	if n := len(h.m.opts.Agent.PlanComments()); n != 2 {
		t.Fatalf("the terminal collected %d comments", n)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "2 waiting") {
		t.Fatalf("the second comment is not counted, view:\n%s", v)
	}

	h.keys("/plan changes")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if !h.m.opts.Agent.PlanMode {
		t.Fatal("asking for changes dropped out of plan mode")
	}
	if n := len(h.m.opts.Agent.PlanComments()); n != 0 {
		t.Fatalf("%d comments survived being sent", n)
	}
	sc := h.m.opts.Agent.Provider.(*scripted)
	reqs := sc.reqs()
	last := reqs[len(reqs)-1]
	got := ""
	for _, msg := range last.Messages {
		if msg.Role == llm.RoleUser {
			got = msg.Text()
		}
	}
	// Both comments and the instruction that starts the revision are in
	// the same user message: the model is told once, not three times.
	for _, want := range []string{"rename it to what?", "which tests?", agent.PlanRevisePrompt} {
		if !strings.Contains(got, want) {
			t.Fatalf("the last user message is missing %q:\n%s", want, got)
		}
	}
	// The revision rewrites the file rather than adding a second plan.
	body, err := plan.Read(root)
	if err != nil || !strings.Contains(body, "rename Parser to Parse") {
		t.Fatalf("plan file = %q err = %v", body, err)
	}
	if strings.Count(body, "## Plan") != 1 {
		t.Fatalf("the plan file grew a second plan:\n%s", body)
	}
}

// Approving is the other way out: the comments go, plan mode goes, and
// the note says what was approved.
func TestPlanApproveClearsTheComments(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "## Plan\n1. rename Parser"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	)
	h.keys("/plan rename the parser")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("/plan comment 2 never sent")
	h.enter()
	h.keys("/plan approve")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if h.m.opts.Agent.PlanMode {
		t.Fatal("still in plan mode after approve")
	}
	if n := len(h.m.opts.Agent.PlanComments()); n != 0 {
		t.Fatalf("%d comments survived approval", n)
	}
}
