package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func runPlanAgent(t *testing.T) (*Agent, *RunPlan, string) {
	t.Helper()
	root := t.TempDir()
	ag := &Agent{
		Tools:       tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, tool.Bash{Root: root}),
		Log:         &session.Memory{},
		AutoApprove: true,
	}
	rp := &RunPlan{Parent: ag}
	ag.Tools.Add(rp)
	return ag, rp, root
}

// A plan that works comes back short: an edit is one line, a passing
// check is its tail, and nothing the model does not need re-enters the
// context.
func TestRunPlanSucceedsAndReportsCompactly(t *testing.T) {
	_, rp, root := runPlanAgent(t)
	big := strings.Repeat("line of file content\n", 200)
	out, err := rp.Run(context.Background(), json.RawMessage(`{"steps":[
{"id":"w1","tool":"write_file","input":{"path":"a.txt","content":`+jsonString(big)+`}},
{"id":"w2","tool":"write_file","input":{"path":"b.txt","content":"two\n"}},
{"id":"t","tool":"bash","input":{"command":"cat a.txt b.txt | wc -l"},"check":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "plan finished: 3 steps") || !strings.Contains(out, "201") {
		t.Fatalf("out = %q", out)
	}
	if strings.Count(out, "line of file content") > 0 || len(out) > 600 {
		t.Fatalf("a successful plan echoed what it wrote (%d bytes):\n%s", len(out), out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(b) != "two\n" {
		t.Fatalf("b.txt = %q", b)
	}
}

// A failing check stops the program: the steps after it never run, the
// model gets the check's whole output, and the not-run steps by name.
func TestRunPlanStopsAtFailedCheck(t *testing.T) {
	_, rp, root := runPlanAgent(t)
	out, err := rp.Run(context.Background(), json.RawMessage(`{"steps":[
{"id":"w1","tool":"write_file","input":{"path":"a.txt","content":"x\n"}},
{"id":"test","tool":"bash","input":{"command":"echo the reason it failed; exit 3"},"check":true},
{"id":"w2","tool":"write_file","input":{"path":"after.txt","content":"no\n"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "✗ test") || !strings.Contains(out, "the reason it failed") || !strings.Contains(out, "not run: w2") {
		t.Fatalf("out = %q", out)
	}
	if _, err := os.Stat(filepath.Join(root, "after.txt")); err == nil {
		t.Fatal("a step after the failed check ran")
	}
}

// A command that is not marked as a check may exit non-zero without
// stopping the plan: exit 1 from grep is an answer.
func TestRunPlanNonCheckExitDoesNotStop(t *testing.T) {
	_, rp, _ := runPlanAgent(t)
	out, err := rp.Run(context.Background(), json.RawMessage(`{"steps":[
{"id":"g","tool":"bash","input":{"command":"exit 1"}},
{"id":"w","tool":"write_file","input":{"path":"a.txt","content":"x\n"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "plan finished: 2 steps") {
		t.Fatalf("out = %q", out)
	}
}

// Every step is a real call: a denial is still a denial, and it stops
// the plan.
func TestRunPlanStepsGoThroughApproval(t *testing.T) {
	ag, rp, root := runPlanAgent(t)
	ag.AutoApprove = false
	ag.Approver = ApproverFunc(func(context.Context, ToolCall) (Decision, error) { return Deny, nil })
	out, err := rp.Run(context.Background(), json.RawMessage(`{"steps":[
{"id":"w","tool":"write_file","input":{"path":"a.txt","content":"x\n"}},
{"id":"w2","tool":"write_file","input":{"path":"b.txt","content":"y\n"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
		t.Fatal("a denied write happened")
	}
	if !strings.Contains(out, "✗ w") || !strings.Contains(out, "not run: w2") {
		t.Fatalf("out = %q", out)
	}
}

func TestRunPlanRejectsBadPlans(t *testing.T) {
	_, rp, _ := runPlanAgent(t)
	for _, in := range []string{
		`{"steps":[]}`,
		`{"steps":[{"id":"a","tool":"read_file","input":{"path":"x"},"after":["b"]},{"id":"b","tool":"read_file","input":{"path":"y"}}]}`,
		`{"steps":[{"id":"a","tool":"read_file","input":{}},{"id":"a","tool":"read_file","input":{}}]}`,
		`{"steps":[{"tool":"run_plan","input":{}}]}`,
	} {
		if _, err := rp.Run(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("accepted %s", in)
		}
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
