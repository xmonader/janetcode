package eval

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
)

// A file that cannot be written into the work directory must fail the task
// with the write error. The err from os.WriteFile used to be assigned to a
// variable scoped to the if that wrote it, so the check below read the
// outer err — nil, from MkdirTemp — and the task ran against a tree that
// was never set up, failing later for a reason with nothing to do with it.
func TestSetupReportsAFileItCouldNotWrite(t *testing.T) {
	r := &Runner{
		NewAgent: func(string, string) (*agent.Agent, error) {
			t.Error("the run started even though setup failed")
			return nil, errors.New("no agent in this test")
		},
	}
	// "a" is created as a directory by the first entry, so writing the
	// second over it cannot work.
	res := r.one(context.Background(), Task{
		Name:  "setup",
		Files: map[string]string{"a/b.txt": "x", "a": "y"},
		Check: "true",
	}, "setup")
	if res.Passed {
		t.Fatal("a task whose files could not be written passed")
	}
	if !strings.HasPrefix(res.Reason, "setup:") {
		t.Fatalf("reason = %q, want the write error", res.Reason)
	}
}

// A task whose agent panics must fail that task, not the whole run: the
// other tasks are already queued and several may have passed.
func TestAPanickingTaskFailsOnlyItself(t *testing.T) {
	r := &Runner{NewAgent: func(string, string) (*agent.Agent, error) { panic("boom") }}
	results := r.Run(context.Background(), []Task{
		{Name: "panics", Prompt: "p", Check: "true"},
		{Name: "second", Prompt: "p", Check: "true"},
	})
	if len(results) != 2 {
		t.Fatalf("both tasks should have a result, got %d", len(results))
	}
	for _, res := range results {
		if res.Passed || !strings.Contains(res.Reason, "panic: boom") {
			t.Fatalf("every task should report its own panic, got %q", res.Reason)
		}
	}
}

// A cancelled run must not drain its whole queue: each remaining task-run
// would otherwise prepare a work directory and a git worktree only to die
// on the dead context.
func TestCancelledRunDoesNotStartQueuedTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var started atomic.Int64
	r := &Runner{
		Jobs: 1,
		NewAgent: func(string, string) (*agent.Agent, error) {
			started.Add(1)
			return nil, errors.New("no agent in this test")
		},
	}
	results := r.Run(ctx, []Task{
		{Name: "one", Prompt: "p", Check: "true"},
		{Name: "two", Prompt: "p", Check: "true"},
		{Name: "three", Prompt: "p", Check: "true"},
	})
	if n := started.Load(); n != 0 {
		t.Fatalf("no task should have been set up after cancellation, %d were", n)
	}
	for _, res := range results {
		if !strings.Contains(res.Reason, "cancelled") {
			t.Fatalf("every task should report the cancellation, got %q", res.Reason)
		}
	}
}
