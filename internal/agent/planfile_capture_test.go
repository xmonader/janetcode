package agent

import (
	"testing"
)

// The plan a plan-mode turn writes (capturePlan) goes to the same file
// the live plan watches. Written by the harness rather than a file tool,
// it is in no change record, so the next look reported it to the model as
// the user having edited the plan while it worked — the model's own plan
// handed back to it as steering.
func TestPlanfileCapturedPlanIsNotTheUsersEdit(t *testing.T) {
	a := &Agent{Model: "m", Root: t.TempDir(), PlanFile: true, PlanMode: true}
	emit := func(Event) {}
	a.planfileCheck(emit) // the first look: no plan yet
	a.capturePlan("Here it is.\n\n## Plan\n1. change the parser\n2. run the tests\n", emit)
	a.planfileCheck(emit)
	if _, harness := a.takeSteering(); len(harness) != 0 {
		t.Fatalf("the harness's own plan write was sent as the user's edit: %q", harness)
	}
}
