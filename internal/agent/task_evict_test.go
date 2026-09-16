package agent

import (
	"context"
	"fmt"
	"testing"
)

// task is not a mutating tool, so several calls run in parallel and a
// fork outlives the call that started it. Evicting by age alone could
// take the worktree out from under an editing subagent that was still
// working in it, and drop the only handle Cancel had on a running fork.
func TestEvictionSkipsChildrenThatAreStillRunning(t *testing.T) {
	tk := &Task{}
	for i := 0; i < maxChildren; i++ {
		tk.remember(context.Background(), fmt.Sprintf("task-%d", i), &subagent{agent: &Agent{}, done: i != 0})
	}
	// task-0 is still running; the oldest finished child is task-1.
	tk.remember(context.Background(), "task-new", &subagent{agent: &Agent{}})
	ids := tk.ChildIDs()
	if len(ids) != maxChildren {
		t.Fatalf("children = %v, want %d of them", ids, maxChildren)
	}
	if ids[0] != "task-0" {
		t.Fatalf("children = %v, want the running task-0 kept", ids)
	}
	for _, id := range ids {
		if id == "task-1" {
			t.Fatalf("children = %v, want the oldest finished child evicted", ids)
		}
	}
}

func TestEvictionKeepsEveryoneWhenNobodyHasFinished(t *testing.T) {
	tk := &Task{}
	for i := 0; i < maxChildren+2; i++ {
		tk.remember(context.Background(), fmt.Sprintf("task-%d", i), &subagent{agent: &Agent{}})
	}
	if n := len(tk.ChildIDs()); n != maxChildren+2 {
		t.Fatalf("kept %d children, want all %d: none of them has finished", n, maxChildren+2)
	}
}
