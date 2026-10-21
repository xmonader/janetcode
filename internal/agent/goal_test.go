package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestFoldGoalsAndStatus(t *testing.T) {
	mem := &session.Memory{}
	a := &Agent{Log: mem}
	id, _ := a.CreateGoal("ship the cache", 3)
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
	ask := AskFunc(func(_ context.Context, qs []Question) ([]Answer, error) {
		if len(qs) != 1 {
			t.Fatalf("qs = %d", len(qs))
		}
		got = qs[0]
		return []Answer{{Label: qs[0].Approve}}, nil
	})
	ans, err := ask.Ask(context.Background(), []Question{{
		Prompt: "Execute this plan?", Detail: "## Plan\n1. do it", Intent: IntentPlanReview,
		Approve: "Approve", Options: []AskOption{{Label: "Approve"}, {Label: "Keep planning"}},
	}})
	if err != nil || len(ans) != 1 || ans[0].Label != "Approve" || got.Intent != IntentPlanReview {
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
	first, _ := a.CreateGoal("one", 0)
	second, _ := a.CreateGoal("two", 0)
	if first == second {
		t.Fatalf("both goals got id %q", first)
	}
	if gs := FoldGoals(a.Log.Events()); len(gs) != 2 {
		t.Fatalf("goals = %+v", gs)
	}
}

func TestPausedGoalIsResumable(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id, _ := a.CreateGoal("one", 0)
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
	id, _ := a.CreateGoal("ship the cache", 0)
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

// A status other than complete or blocked used to close the goal as
// complete: a model saying the work was still in progress ended it.
func TestGoalStatusRefusesAnUnknownStatus(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id, _ := a.CreateGoal("ship the cache", 0)
	tl := &GoalStatus{Parent: a}
	if _, err := tl.Run(context.Background(), []byte(`{"status":"in_progress"}`)); err == nil {
		t.Fatal("an unknown status was accepted")
	}
	if g := ActiveGoal(a.Log.Events()); g == nil || g.ID != id {
		t.Fatalf("active goal = %+v, want %s still active", g, id)
	}
}

func TestGoalStatusOnABlockedGoalReportsTheReason(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id, _ := a.CreateGoal("ship the cache", 0)
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

func TestTargetGoalPauseAndResume(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id, _ := a.CreateGoal("ship the cache", 0)
	g, phase, err := TargetGoal(a.Log.Events(), "pause")
	if err != nil || g.ID != id || phase != GoalPaused {
		t.Fatalf("g=%+v phase=%q err=%v", g, phase, err)
	}
	a.SetGoalPhase(id, phase, "")
	g, phase, err = TargetGoal(a.Log.Events(), "resume")
	if err != nil || g.ID != id || phase != GoalActive {
		t.Fatalf("resume: g=%+v phase=%q err=%v", g, phase, err)
	}
}

func TestTargetGoalDoneFallsBackToMostRecent(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id, _ := a.CreateGoal("ship the cache", 0)
	a.SetGoalPhase(id, GoalPaused, "")
	g, phase, err := TargetGoal(a.Log.Events(), "done")
	if err != nil || g.ID != id || phase != GoalComplete {
		t.Fatalf("g=%+v phase=%q err=%v", g, phase, err)
	}
}

func TestTargetGoalWithNoGoalIsAnError(t *testing.T) {
	if _, _, err := TargetGoal(nil, "pause"); err == nil {
		t.Fatal("want error with no goal at all")
	}
}

func TestFormatGoalsIncludesReason(t *testing.T) {
	gs := []Goal{{ID: "g1", Phase: GoalBlocked, Reason: "no credentials", Rounds: 1, MaxRounds: 8, Objective: "ship it"}}
	out := FormatGoals(gs)
	if !strings.Contains(out, "g1") || !strings.Contains(out, "ship it") || !strings.Contains(out, "no credentials") {
		t.Fatalf("out = %q", out)
	}
}

func TestGoalStatusWithNoGoalTellsTheModelToJustAnswer(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	tl := &GoalStatus{Parent: a}
	out, err := tl.Run(context.Background(), []byte(`{"status":"complete"}`))
	if err != nil {
		t.Fatalf("a stray goal_status call must not show as a failed tool: %v", err)
	}
	if !strings.Contains(out, "No goal is set") || !strings.Contains(out, "do not call goal_status") {
		t.Fatalf("out = %q", out)
	}
	if len(FoldGoals(a.Log.Events())) != 0 {
		t.Fatal("a stray call must not create a goal")
	}
}

func TestGoalContinuationWithoutAdvisorModelDoesNoIO(t *testing.T) {
	a := &Agent{Log: &session.Memory{}, AdvisorModel: ""}
	a.CreateGoal("ship it", 2)
	got := a.GoalContinuation(context.Background())
	if !strings.Contains(got, "ship it") || strings.Contains(got, "[planner]") {
		t.Fatalf("got %q", got)
	}
}

// The goal passed to goalRoleNote is the one before its round is counted,
// so Rounds is how many rounds have finished. Only round one has none
// behind it: round two used to be told "this is the first working round"
// by the planner, and the skeptic never saw the round that just ended.
func TestGoalRoleNoteSecondRoundGetsTheSkeptic(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("one step"), text("still unproven")}}
	a := &Agent{Provider: f, Model: "m", AdvisorModel: "m", Log: &session.Memory{}}
	a.CreateGoal("ship it", 3)
	if got := a.GoalContinuation(context.Background()); !strings.Contains(got, "[planner]") {
		t.Fatalf("round one: %q", got)
	}
	if got := a.GoalContinuation(context.Background()); !strings.Contains(got, "[skeptic]") {
		t.Fatalf("round two: %q", got)
	}
	if sys := f.requests[1].System; sys != goalSkepticSystem {
		t.Fatalf("round two asked the planner: %q", sys)
	}
}

func TestGoalRoleNoteSkipsWhenContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &Agent{AdvisorModel: "z-ai/glm-5.3"}
	if note := a.goalRoleNote(ctx, Goal{Objective: "ship it", Rounds: 0, MaxRounds: 3}); note != "" {
		t.Fatalf("cancelled ctx still queried: %q", note)
	}
	// A nil context is exactly what is under test here: goalRoleNote must
	// refuse rather than invent a context.Background() of its own.
	//lint:ignore SA1012 the nil context is the case being asserted
	if note := a.goalRoleNote(nil, Goal{Objective: "ship it"}); note != "" {
		t.Fatalf("nil ctx invented a Background query: %q", note)
	}
}

// On a recorded session a second goal was started while the first was
// still active. The model's goal_status complete acts on the newest
// active goal, so it completed the accident; the real goal then got a
// round of its own, in which the model reported the work already done and
// nothing happened. One goal is active at a time now.
func TestANewGoalPausesTheActiveOne(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	first, paused := a.CreateGoal("create a small tcp proxy in python and test it", 0)
	if len(paused) != 0 {
		t.Fatalf("the first goal paused %v", paused)
	}
	second, paused := a.CreateGoal("objective", 0)
	if len(paused) != 1 || paused[0] != first {
		t.Fatalf("paused = %v, want [%s]", paused, first)
	}
	if g := ActiveGoal(a.Log.Events()); g == nil || g.ID != second {
		t.Fatalf("active = %+v, want %s", g, second)
	}
	// Completing the new goal leaves nothing running: the paused one does
	// not quietly claim the next round.
	a.SetGoalPhase(second, GoalComplete, "")
	if g := ActiveGoal(a.Log.Events()); g != nil {
		t.Fatalf("active after completing the goal = %+v", g)
	}
	if got := a.GoalContinuation(context.Background()); got != "" {
		t.Fatalf("continuation = %q", got)
	}
	// It is still there to come back to.
	if g := LastGoal(a.Log.Events(), GoalPaused); g == nil || g.ID != first {
		t.Fatalf("paused goal = %+v", g)
	}
}

// The round budget was 8 and nothing could change it: both front ends
// passed 0 to CreateGoal, and when the eight were spent the loop simply
// stopped — no round sent, nothing said, and the goal still reading
// "active" in the status line and in /goal for the rest of the session.
func TestGoalRoundBudgetIsSettableAndSaysWhenItIsSpent(t *testing.T) {
	// A leading number is an objective, not a budget.
	if n, rest, ok := ParseGoalRounds("3 bugs need fixing"); ok || rest != "3 bugs need fixing" || n != 0 {
		t.Fatalf("ParseGoalRounds took a bare number: %d %q %v", n, rest, ok)
	}
	for _, arg := range []string{"rounds=3 ship the parser", "rounds 3 ship the parser"} {
		n, rest, ok := ParseGoalRounds(arg)
		if !ok || n != 3 || rest != "ship the parser" {
			t.Fatalf("ParseGoalRounds(%q) = %d %q %v", arg, n, rest, ok)
		}
	}
	if n, rest, ok := ParseGoalRounds("rounds 20"); !ok || n != 20 || rest != "" {
		t.Fatalf("a bare re-budget = %d %q %v", n, rest, ok)
	}
	if n, _, _ := ParseGoalRounds("rounds 99999"); n != maxGoalRounds {
		t.Fatalf("an unbounded budget = %d", n)
	}

	a := &Agent{Log: &session.Memory{}}
	id, _ := a.CreateGoal("ship the parser", 2)
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		if got := a.GoalContinuation(ctx); got == "" {
			t.Fatalf("round %d was refused", i)
		}
	}
	// The budget is spent: no round, and the goal is paused saying why
	// rather than sitting there looking active.
	if got := a.GoalContinuation(ctx); got != "" {
		t.Fatalf("a third round on a budget of two: %q", got)
	}
	g, spent := a.SpentGoal()
	if !spent || g.ID != id || g.Rounds != 2 {
		t.Fatalf("spent = %+v %v", g, spent)
	}
	if ActiveGoal(a.Log.Events()) != nil {
		t.Fatal("a spent goal is still active")
	}
	if note := SpentNote(g); !strings.Contains(note, "2 of 2 rounds") || !strings.Contains(note, "/goal rounds 4") {
		t.Fatalf("note = %q", note)
	}
	if _, again := a.SpentGoal(); again {
		t.Fatal("the same spent goal was announced twice")
	}

	// More rounds carry it on — /goal resume alone could not, because the
	// loop refuses a round while the budget is spent.
	a.SetGoalRounds(id, 4)
	if g := ActiveGoal(a.Log.Events()); g == nil || g.MaxRounds != 4 || g.Rounds != 2 {
		t.Fatalf("after a re-budget = %+v", g)
	}
	if got := a.GoalContinuation(ctx); got == "" {
		t.Fatal("no round after the budget was raised")
	}
}

// A recorded session called goal_status complete at step 19 and the turn
// came back "check failed" at step 20: the suite it had just written was
// red, the two facts were reported side by side, and the goal closed
// anyway.
func TestAGoalCompletedOnARedCheckIsReopenedOnce(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	id, _ := a.CreateGoal("make a lightweight testing library in python", 8)
	a.turnN = 1
	a.SetGoalPhase(id, GoalComplete, "")

	red := TurnOutcome{Verdict: VerdictCheckFailed, Why: "2 file(s) edited and a check came back red"}
	var notes []string
	emit := func(e Event) { notes = append(notes, e.Text) }
	a.ReopenCompletedGoal(red, emit)

	g := ActiveGoal(a.Log.Events())
	if g == nil || g.ID != id {
		t.Fatalf("the goal was not reopened: %+v", FoldGoals(a.Log.Events()))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "reopened") {
		t.Fatalf("notes = %v", notes)
	}
	// The next round says what happened and what to do about it.
	round := goalPrompt(*g)
	for _, want := range []string{"put it back", "came back red", "goal_status blocked"} {
		if !strings.Contains(round, want) {
			t.Fatalf("the round prompt lacks %q:\n%s", want, round)
		}
	}

	// Once. The model may have a reason the harness cannot see, and a
	// harness that argued every round would spend the whole budget on it.
	a.turnN = 2
	a.SetGoalPhase(id, GoalComplete, "")
	notes = nil
	a.ReopenCompletedGoal(red, emit)
	if g := ActiveGoal(a.Log.Events()); g != nil {
		t.Fatalf("reopened twice: %+v", g)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}

	// A green turn never reopens anything.
	b := &Agent{Log: &session.Memory{}}
	bid, _ := b.CreateGoal("ship it", 8)
	b.turnN = 1
	b.SetGoalPhase(bid, GoalComplete, "")
	b.ReopenCompletedGoal(TurnOutcome{Verdict: VerdictVerified, Why: "tests ran and passed"}, nil)
	if ActiveGoal(b.Log.Events()) != nil {
		t.Fatal("a goal completed on a green turn was reopened")
	}
}

// The whole path, through a real turn: the model edits a file, runs a
// check that comes back red, and calls goal_status complete. The goal is
// active again when the turn ends, and the next round is told why.
func TestATurnThatClosesAGoalOnARedCheckIsPutBack(t *testing.T) {
	root := t.TempDir()
	a := &Agent{Model: "m", Log: &session.Memory{}, AutoApprove: true, Root: root}
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root}, &GoalStatus{Parent: a})
	a.Provider = &fake{responses: []*llm.Response{
		toolCall("w1", "write_file", `{"path":"`+filepath.Join(root, "a.py")+`","content":"x = 1\n"}`),
		toolCall("b1", "bash", `{"command":"exit 1 # run the tests"}`),
		toolCall("g1", "goal_status", `{"status":"complete"}`),
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	id, _ := a.CreateGoal("make the tests green", 3)

	var notes []string
	if err := a.Run(context.Background(), a.GoalContinuation(context.Background()), func(e Event) {
		if e.Kind == EvNotice {
			notes = append(notes, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}

	g := ActiveGoal(a.Log.Events())
	if g == nil || g.ID != id {
		t.Fatalf("the goal closed on a red check: %+v", FoldGoals(a.Log.Events()))
	}
	if !strings.HasPrefix(g.Reason, GoalReopened) {
		t.Fatalf("goal = %+v", g)
	}
	found := false
	for _, n := range notes {
		if strings.Contains(n, "reopened") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the reopen was not announced: %v", notes)
	}
	if next := a.GoalContinuation(context.Background()); !strings.Contains(next, "put it back") {
		t.Fatalf("the next round does not say why:\n%s", next)
	}
}
