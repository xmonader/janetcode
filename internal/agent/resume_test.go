package agent

import (
	"context"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// answer scripts one plain assistant reply.
func answer(text string) *llm.Response {
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, text), StopReason: llm.StopEndTurn}
}

// Everything resume restores comes off the session file, and the only
// thing that proves it is a round trip through one. These are the two
// pieces of state an agent carries across a restart that are not in the
// message history itself: the plan-mode flag and the turn counter.
func TestResumeRestoresPlanModeAndTurnCounter(t *testing.T) {
	dir := t.TempDir()
	s, err := session.New(dir, "/work", "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	a := &Agent{Provider: &fake{responses: []*llm.Response{answer("one"), answer("two")}}, Model: "m", Log: s}
	a.SetPlanMode(true)
	for i := 0; i < 2; i++ {
		if err := a.Run(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
	}
	if a.turnN != 2 {
		t.Fatalf("the live agent is on turn %d", a.turnN)
	}

	// A new process: the session is reopened from the file and an agent
	// is pointed at it.
	reopened, _, err := session.Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := &Agent{Provider: &fake{responses: []*llm.Response{answer("three")}}, Model: "m"}
	b.SetLog(reopened)

	if !b.PlanMode {
		t.Fatal("plan mode was not restored: a resumed session would start editing files the user had not approved")
	}
	if b.turnN != 2 {
		t.Fatalf("turn counter restored as %d, want 2", b.turnN)
	}
	// The next turn must not reuse a number an earlier run already wrote,
	// or the trajectory and the undo checkpoints key two turns the same.
	if err := b.Run(context.Background(), "again", nil); err != nil {
		t.Fatal(err)
	}
	if b.turnN != 3 {
		t.Fatalf("the resumed run wrote turn %d", b.turnN)
	}
	seen := map[int]bool{}
	for _, e := range reopened.Events() {
		if e.Type == session.TypeTurnStart {
			if seen[e.Turn] {
				t.Fatalf("turn %d started twice in one session log", e.Turn)
			}
			seen[e.Turn] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("turn starts = %v, want three distinct turns", seen)
	}

	// Plan mode turned off before the restart stays off after it.
	b.SetPlanMode(false)
	again, _, err := session.Open(dir, s.Meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	c := &Agent{Model: "m"}
	c.SetLog(again)
	if c.PlanMode {
		t.Fatal("plan mode came back on after being turned off")
	}
}
