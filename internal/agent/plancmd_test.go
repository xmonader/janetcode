package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/plan"
)

func planAgent(t *testing.T, f *fake) (*Agent, string) {
	t.Helper()
	a, root := newAgent(t, f, nil)
	a.Root = root
	a.SetPlanMode(true)
	return a, root
}

func TestAPlanTurnWritesThePlanFileAndARevisionRewritesIt(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		text("I read the parser.\n\n## Plan\n1. rename it\n2. test it"),
		text("Right.\n\n## Plan\n1. rename it to Parse\n2. test it"),
	}}
	a, root := planAgent(t, f)
	_, emit := collect()
	if err := a.Run(context.Background(), "plan the rename", emit); err != nil {
		t.Fatal(err)
	}
	body, err := plan.Read(root)
	if err != nil {
		t.Fatalf("no plan file after a plan turn: %v", err)
	}
	if !strings.HasPrefix(body, "## Plan") || strings.Contains(body, "I read the parser") {
		t.Fatalf("plan file = %q", body)
	}
	if err := a.Run(context.Background(), "rename it to Parse", emit); err != nil {
		t.Fatal(err)
	}
	body, _ = plan.Read(root)
	if !strings.Contains(body, "rename it to Parse") {
		t.Fatalf("the revision did not reach the file: %q", body)
	}
	prev, err := os.ReadFile(plan.PrevFile(root))
	if err != nil || !strings.Contains(string(prev), "1. rename it\n") {
		t.Fatalf("previous version = %q err = %v", prev, err)
	}
}

func TestAReplyWithNoPlanLeavesTheFileAlone(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("Which parser did you mean?")}}
	a, root := planAgent(t, f)
	_, emit := collect()
	if err := a.Run(context.Background(), "plan it", emit); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Read(root); err == nil {
		t.Fatal("a question was written down as a plan")
	}
}

func TestPlanReviewNumbersTheLinesOfTheFileItReadsBack(t *testing.T) {
	a, root := planAgent(t, &fake{})
	if _, err := plan.Write(root, "## Plan\n1. rename it\n2. test it"); err != nil {
		t.Fatal(err)
	}
	res, err := a.PlanCommand("review", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 │ ## Plan", "2 │ 1. rename it", "3 │ 2. test it"} {
		if !strings.Contains(res.Sheet, want) {
			t.Errorf("review is missing %q:\n%s", want, res.Sheet)
		}
	}
	// The file is the plan: an edit made outside the session shows up on
	// the next review, because nothing kept a second copy.
	if err := os.WriteFile(plan.File(root), []byte("## Plan\nedited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ = a.PlanCommand("review", false)
	if !strings.Contains(res.Sheet, "edited by hand") || strings.Contains(res.Sheet, "rename it") {
		t.Fatalf("review showed a copy, not the file:\n%s", res.Sheet)
	}
}

func TestCommentsCollectAndReachTheModelAsOneMessage(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("## Plan\nrevised")}}
	a, root := planAgent(t, f)
	plan.Write(root, "## Plan\n1. rename it\n2. test it")
	for _, arg := range []string{"comment 2 rename it to what?", "comment plan.md:3 which tests?"} {
		res, err := a.PlanCommand(arg, false)
		if err != nil || res.Error != "" {
			t.Fatalf("%s → %q %v", arg, res.Error, err)
		}
	}
	if n := len(a.PlanComments()); n != 2 {
		t.Fatalf("collected %d comments", n)
	}
	// A review shows them against their lines before they are sent.
	if res, _ := a.PlanCommand("review", false); !strings.Contains(res.Sheet, "↳ rename it to what?") {
		t.Fatalf("review does not show the comment:\n%s", res.Sheet)
	}
	res, err := a.PlanCommand("changes", false)
	if err != nil || res.Error != "" {
		t.Fatalf("changes → %q %v", res.Error, err)
	}
	if !a.Settings().PlanMode {
		t.Fatal("asking for changes left plan mode")
	}
	if len(a.PlanComments()) != 0 {
		t.Fatal("the comments were not cleared once sent")
	}
	if res.Handover == "" {
		t.Fatal("nothing starts the revision turn")
	}
	_, emit := collect()
	if err := a.Run(context.Background(), res.Handover, emit); err != nil {
		t.Fatal(err)
	}
	var users []llm.Message
	for _, m := range f.requests[0].Messages {
		if m.Role == llm.RoleUser {
			users = append(users, m)
		}
	}
	if len(users) != 1 {
		t.Fatalf("the comments reached the model as %d messages, not one: %+v", len(users), users)
	}
	got := users[0].Text()
	for _, want := range []string{"rename it to what?", "which tests?", "1. rename it", "2. test it", PlanRevisePrompt} {
		if !strings.Contains(got, want) {
			t.Errorf("the message is missing %q:\n%s", want, got)
		}
	}
}

func TestChangesSteersATurnThatIsStillRunning(t *testing.T) {
	a, root := planAgent(t, &fake{})
	plan.Write(root, "## Plan\n1. rename it")
	a.PlanCommand("comment 2 too vague", false)
	res, err := a.PlanCommand("changes", true)
	if err != nil || res.Error != "" || res.Handover != "" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	a.steerMu.Lock()
	steer := a.steer
	a.steerMu.Unlock()
	if len(steer) != 1 || !strings.Contains(steer[0], "too vague") {
		t.Fatalf("steering = %q", steer)
	}
}

func TestApproveClearsTheCommentsAndLeavesPlanMode(t *testing.T) {
	a, root := planAgent(t, &fake{})
	plan.Write(root, "## Plan\n1. rename it")
	a.PlanCommand("comment 2 never sent", false)
	res, err := a.PlanCommand("approve", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Handover != PlanApprovedPrompt {
		t.Fatalf("handover = %q", res.Handover)
	}
	if a.Settings().PlanMode {
		t.Fatal("still in plan mode after approve")
	}
	if len(a.PlanComments()) != 0 {
		t.Fatal("approve left comments behind to be sent later")
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], plan.Name) {
		t.Fatalf("approve does not say the plan file was approved: %+v", res.Notes)
	}
	if res, _ := a.PlanCommand("approve", false); !strings.Contains(res.Error, "not in plan mode") {
		t.Fatalf("approving twice = %+v", res)
	}
}

func TestPlanCommandRefusesWhatItCannotDoYet(t *testing.T) {
	a, _ := planAgent(t, &fake{})
	for _, tc := range []struct{ arg, want string }{
		{"review", "no plan yet"},
		{"comment 2 hello", "no plan to comment on"},
		{"changes", "no comments collected"},
	} {
		res, err := a.PlanCommand(tc.arg, false)
		if err != nil || !strings.Contains(res.Error, tc.want) {
			t.Errorf("%s → %q %v, want %q", tc.arg, res.Error, err, tc.want)
		}
	}
}

func TestPlanCommandKeepsTheModeItAlwaysHad(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	a.Root = t.TempDir()
	res, err := a.PlanCommand("", false)
	if err != nil || !a.Settings().PlanMode || res.Handover != "" {
		t.Fatalf("/plan alone: mode = %v res = %+v", a.Settings().PlanMode, res)
	}
	res, _ = a.PlanCommand("rewrite the parser", false)
	if res.Handover != "rewrite the parser" {
		t.Fatalf("/plan <request> handover = %q", res.Handover)
	}
	res, _ = a.PlanCommand("off", false)
	if a.Settings().PlanMode || len(res.Notes) == 0 {
		t.Fatalf("/plan off: mode = %v res = %+v", a.Settings().PlanMode, res)
	}
}

// The verbs that take no argument are ordinary English words, and a
// request that happened to start with one was taken for the verb: "/plan
// go through the handlers…" approved the plan (or refused, out of plan
// mode), "/plan read the config loader…" showed the review, and "/plan
// exit codes are wrong…" turned plan mode off. The request was lost each
// time.
func TestPlanRequestStartingWithAVerbIsARequest(t *testing.T) {
	for _, req := range []string{
		"go through every handler and add logging",
		"read the config loader and propose a split",
		"exit codes are wrong in headless mode, fix them",
		"changes to the parser should be staged",
		"review the auth middleware",
	} {
		a, _ := newAgent(t, &fake{}, nil)
		a.Root = t.TempDir()
		res, err := a.PlanCommand(req, false)
		if err != nil || res.Handover != req || !a.Settings().PlanMode {
			t.Errorf("/plan %s: handover = %q, mode = %v, res = %+v", req, res.Handover, a.Settings().PlanMode, res)
		}
	}
}
