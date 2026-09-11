package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
)

func TestFoldGoalsAndStatus(t *testing.T) {
	mem := &session.Memory{}
	a := &Agent{Log: mem}
	id := a.CreateGoal("ship the cache", 3)
	if g := ActiveGoal(mem.Events()); g == nil || g.ID != id || g.Phase != GoalActive {
		t.Fatalf("active = %+v", g)
	}
	a.SetGoalPhase(id, GoalComplete, "")
	if ActiveGoal(mem.Events()) != nil {
		t.Fatal("still active")
	}
	gs := FoldGoals(mem.Events())
	if len(gs) != 1 || gs[0].Phase != GoalComplete {
		t.Fatalf("fold = %+v", gs)
	}
}

func TestAskerPlanReview(t *testing.T) {
	var got Question
	ask := AskFunc(func(_ context.Context, q Question) (Answer, error) {
		got = q
		return Answer{Label: q.Approve}, nil
	})
	ans, err := ask.Ask(context.Background(), Question{
		Prompt: "Execute this plan?", Detail: "## Plan\n1. do it", Intent: IntentPlanReview,
		Approve: "Approve", Options: []AskOption{{Label: "Approve"}, {Label: "Keep planning"}},
	})
	if err != nil || ans.Label != "Approve" || got.Intent != IntentPlanReview {
		t.Fatalf("ans=%+v got=%+v err=%v", ans, got, err)
	}
}

func TestFoldGoalsIgnoresNonGoalEvents(t *testing.T) {
	events := []session.Event{
		{Seq: 1, Type: session.TypeToolCall, Name: "bash", Arguments: "ls"},
		{Seq: 2, Type: session.TypeToolResult, Name: "bash", CallID: "c1"},
		{Seq: 3, Type: session.TypeSpillSave, Name: "read_file"},
		{Seq: 4, Type: session.TypeGoalCreate, Name: "g1", Output: "ship it"},
		{Seq: 5, Type: session.TypeGoalPhase, Name: "g1", Reason: GoalActive},
	}
	gs := FoldGoals(events)
	if len(gs) != 1 || gs[0].ID != "g1" || gs[0].Objective != "ship it" {
		t.Fatalf("goals = %+v", gs)
	}
	if g := ActiveGoal(events); g == nil || g.ID != "g1" {
		t.Fatalf("active = %+v", g)
	}
}

func TestGoalIDsAreUniqueWithinATurn(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	first := a.CreateGoal("one", 0)
	second := a.CreateGoal("two", 0)
	if first == second {
		t.Fatalf("both goals got id %q", first)
	}
	if gs := FoldGoals(a.Log.Events()); len(gs) != 2 {
		t.Fatalf("goals = %+v", gs)
	}
}

func TestPausedGoalIsResumable(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id := a.CreateGoal("one", 0)
	a.SetGoalPhase(id, GoalPaused, "")
	if ActiveGoal(a.Log.Events()) != nil {
		t.Fatal("paused goal is still active")
	}
	g := LastGoal(a.Log.Events(), GoalPaused)
	if g == nil || g.ID != id {
		t.Fatalf("paused goal = %+v", g)
	}
	a.SetGoalPhase(g.ID, GoalActive, "")
	if got := ActiveGoal(a.Log.Events()); got == nil || got.ID != id {
		t.Fatalf("resumed goal = %+v", got)
	}
}

func TestGoalStatusTwiceIsNotAnError(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id := a.CreateGoal("ship the cache", 0)
	tl := &GoalStatus{Parent: a}
	if _, err := tl.Run(context.Background(), []byte(`{"status":"complete"}`)); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// The second call is the model confirming what it already did. An
	// error there sends it round again; a plain statement of the phase
	// does not.
	out, err := tl.Run(context.Background(), []byte(`{"status":"complete"}`))
	if err != nil {
		t.Fatalf("second call returned an error: %v", err)
	}
	if !strings.Contains(out, id) || !strings.Contains(out, GoalComplete) {
		t.Fatalf("second call said %q, want the id and the phase", out)
	}
}

func TestGoalStatusOnABlockedGoalReportsTheReason(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id := a.CreateGoal("ship the cache", 0)
	tl := &GoalStatus{Parent: a}
	if _, err := tl.Run(context.Background(), []byte(`{"status":"blocked","reason":"no credentials"}`)); err != nil {
		t.Fatalf("first call: %v", err)
	}
	out, err := tl.Run(context.Background(), []byte(`{"status":"complete"}`))
	if err != nil {
		t.Fatalf("second call returned an error: %v", err)
	}
	if !strings.Contains(out, id) || !strings.Contains(out, GoalBlocked) || !strings.Contains(out, "no credentials") {
		t.Fatalf("second call said %q, want the id, the phase and the reason", out)
	}
}

func TestGoalStatusWithNoGoalAtAllStillErrors(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	tl := &GoalStatus{Parent: a}
	if _, err := tl.Run(context.Background(), []byte(`{"status":"complete"}`)); err == nil {
		t.Fatal("marking a goal that was never created should be an error")
	}
}
