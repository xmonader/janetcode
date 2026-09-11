package eval

import (
	"context"
	"errors"
	"strings"
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
		NewAgent: func(string) (*agent.Agent, error) {
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
	})
	if res.Passed {
		t.Fatal("a task whose files could not be written passed")
	}
	if !strings.HasPrefix(res.Reason, "setup:") {
		t.Fatalf("reason = %q, want the write error", res.Reason)
	}
}
