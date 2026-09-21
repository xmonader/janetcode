package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/jev"
)

func TestStuckStateKeepsTheWindowAndTheBudget(t *testing.T) {
	steps := make([]StuckStep, 0, 20)
	for i := 0; i < 20; i++ {
		steps = append(steps, StuckStep{
			Tool:    "edit_file",
			Args:    `{"path":"pkg/file` + string(rune('a'+i%26)) + `.go"}`,
			Failed:  i%3 == 0,
			Outcome: strings.Repeat("noise ", 200),
		})
	}
	got := StuckState(strings.Repeat("goal ", 500), steps)
	// The budget is in runes, which is what textfmt.Clip counts.
	if n := len([]rune(got)); n > stuckStateBudget {
		t.Fatalf("state is %d runes, budget is %d", n, stuckStateBudget)
	}
	// Only the window's worth of numbered lines may appear, and the
	// numbering restarts at 1 for the window, not at the step's index in
	// the whole session.
	if n := strings.Count(got, "\n1. "); n != 1 {
		t.Fatalf("window does not start at 1:\n%s", got)
	}
	if strings.Contains(got, "\n9. ") {
		t.Fatalf("window is longer than %d steps:\n%s", StuckWindow, got)
	}
	if !strings.HasPrefix(got, "Goal: ") {
		t.Fatalf("state does not lead with the goal: %q", got[:20])
	}
}

func TestStuckStateMarksFailures(t *testing.T) {
	got := StuckState("fix the parser", []StuckStep{
		{Tool: "read_file", Args: `{"path":"a.go"}`, Outcome: "12 lines"},
		{Tool: "edit_file", Args: `{"path":"a.go"}`, Failed: true, Outcome: "old_string not found"},
	})
	if !strings.Contains(got, "-> ERROR: old_string not found") {
		t.Fatalf("failed call is not marked:\n%s", got)
	}
	if strings.Contains(got, "-> ERROR: 12 lines") {
		t.Fatalf("successful call marked as an error:\n%s", got)
	}
	if empty := StuckState("g", nil); !strings.Contains(empty, "none yet") {
		t.Fatalf("empty window: %q", empty)
	}
}

func TestStuckActionReadsTheThreshold(t *testing.T) {
	res := &jev.Result{Answers: map[string]jev.Answer{
		qStuck:            {Kind: jev.Noul, Noul: 0.72},
		qOnGoal:           {Kind: jev.Noul, Noul: 0.31},
		qNextCallWillFail: {Kind: jev.Noul, Noul: 0.88},
	}}
	flag, stuck, onGoal, next := StuckAction(res, 0.7)
	if !flag || stuck != 0.72 || onGoal != 0.31 || next != 0.88 {
		t.Fatalf("flag=%v stuck=%v on_goal=%v next=%v", flag, stuck, onGoal, next)
	}
	if flag, _, _, _ := StuckAction(res, 0.9); flag {
		t.Fatal("0.72 must not flag at a 0.9 threshold")
	}
	if flag, _, _, _ := StuckAction(nil, 0.5); flag {
		t.Fatal("a nil result must not flag")
	}
}

func TestStuckQuestionsAreNoulsWithBothCriteria(t *testing.T) {
	qs := StuckQuestions()
	if len(qs) != 3 {
		t.Fatalf("%d questions", len(qs))
	}
	for _, q := range qs {
		if q.Kind != jev.Noul {
			t.Errorf("%s: kind %q", q.ID, q.Kind)
		}
		if q.NoulCriteria["true"] == "" || q.NoulCriteria["false"] == "" {
			t.Errorf("%s: criteria %v", q.ID, q.NoulCriteria)
		}
	}
}
