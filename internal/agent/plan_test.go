package agent

import (
	"strings"
	"testing"
)

func TestBlockedByMustNameAStepAndNotACycle(t *testing.T) {
	ok, err := validateTodos([]Todo{
		{Text: "write the migration", Status: TodoPending},
		{Text: "run it in staging", Status: TodoPending, BlockedBy: []string{"write the migration"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ok[1].BlockedBy) != 1 {
		t.Fatalf("todos = %+v", ok)
	}
	_, err = validateTodos([]Todo{
		{Text: "run it", Status: TodoPending, BlockedBy: []string{"write it"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not a step in this plan") {
		t.Fatalf("err = %v", err)
	}
	_, err = validateTodos([]Todo{
		{Text: "a", Status: TodoPending, BlockedBy: []string{"b"}},
		{Text: "b", Status: TodoPending, BlockedBy: []string{"c"}},
		{Text: "c", Status: TodoPending, BlockedBy: []string{"a"}},
	})
	if err == nil || !strings.Contains(err.Error(), "wait for each other") {
		t.Fatalf("a cycle was accepted: %v", err)
	}
}

func TestRenderedPlanShowsWhatEachStepWaitsFor(t *testing.T) {
	got := RenderTodos([]Todo{
		{Text: "write it", Status: TodoDone},
		{Text: "run it", Status: TodoPending, BlockedBy: []string{"write it"}},
	})
	if !strings.Contains(got, "(after: write it)") {
		t.Fatalf("plan = %q", got)
	}
	if strings.Contains(strings.SplitN(got, "\n", 2)[0], "after") {
		t.Fatalf("a finished step does not wait for anything: %q", got)
	}
}

// The plan carried across a compaction is re-read on every later
// request, so it is capped; the whole plan stays one todo call away.
func TestCompactionPlanNoteIsCapped(t *testing.T) {
	var todos []Todo
	for i := 0; i < 60; i++ {
		todos = append(todos, Todo{Text: strings.Repeat("step ", 6) + string(rune('a'+i%26)), Status: TodoPending})
	}
	got := renderOpenPlan(todos)
	if len(got) > maxPlanNote+80 {
		t.Fatalf("plan note is %d characters", len(got))
	}
	if !strings.Contains(got, "more steps; call todo") {
		t.Fatalf("the note does not say what it left out: %q", got)
	}
	short := renderOpenPlan(todos[:2])
	if strings.Contains(short, "more steps") {
		t.Fatalf("a short plan was truncated: %q", short)
	}
}
