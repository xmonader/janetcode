package agent

import (
	"context"
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
