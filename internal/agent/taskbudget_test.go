package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// A subagent runs its own model calls, on the parent's account. Until
// they were billed to the parent, the session total, /stats and the cost
// ceiling all stopped at what the main conversation itself paid — so a
// run under --max-cost could delegate its way past the ceiling.
func TestSubagentSpendIsBilledToTheParent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, task)

	if err := parent.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	kids := task.Usage()
	if kids.InputTokens == 0 {
		t.Fatal("the children spent nothing; the test proves nothing")
	}
	session := parent.UsageSnapshot()
	if session.InputTokens < kids.InputTokens || session.OutputTokens < kids.OutputTokens {
		t.Fatalf("session usage %+v does not include the subagents' %+v", session, kids)
	}
}

// spendyChild delegates once, and the child then reads a file over and
// over at $0.40 a call.
type spendyChild struct {
	mu         sync.Mutex
	childCalls int
}

func (s *spendyChild) Name() string { return "fake" }
func (s *spendyChild) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if req.Messages[0].Text() == "go" {
		if len(req.Messages) == 1 {
			return toolCall("t1", "task", `{"description":"dig","prompt":"dig deep"}`), nil
		}
		return text("done"), nil
	}
	s.mu.Lock()
	s.childCalls++
	n := s.childCalls
	s.mu.Unlock()
	return costly(0.40, fmt.Sprintf("r%d", n), "read_file", `{"path":"a.txt"}`), nil
}

// A child's spend reached the parent only when the child's run ended, so
// the parent's ceiling — checked after every one of its own calls — did
// not bound a subagent at all: under a $1 budget one delegated job made
// every call its iteration limit allowed.
func TestASubagentStopsAtTheParentsCeiling(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &spendyChild{}
	parent := &Agent{Provider: p, Model: "m", System: "sys", AutoApprove: true}
	parent.MaxCostUSD = 1
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)

	err := parent.Run(context.Background(), "go", nil)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want the parent stopped on its budget", err)
	}
	if p.childCalls != 3 {
		t.Fatalf("the child made %d calls at $0.40 under a $1 ceiling, want 3", p.childCalls)
	}
}
