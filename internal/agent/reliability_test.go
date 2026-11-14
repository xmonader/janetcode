package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

func TestEmptyToolStopRunsVerification(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("w", "write_file", `{"path":"a.txt","content":"changed"}`),
		{Message: llm.TextMessage(llm.RoleAssistant, ""), StopReason: llm.StopToolUse},
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove, a.Log = true, &session.Memory{}
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"echo checked > verification-marker"}}}
	if err := a.Run(context.Background(), "change and verify", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "verification-marker")); err != nil {
		t.Fatal("turn ended without verification")
	}
}

func TestReadWindowsDoNotTriggerStuck(t *testing.T) {
	var d loopDetector
	for i := 0; i < 12; i++ {
		for _, path := range []string{"large.go", "other.go"} {
			_, stop := d.record("read_file", []byte(fmt.Sprintf(`{"path":%q,"offset":%d,"limit":100}`, path, i*100+1)), false, false)
			if stop {
				t.Fatalf("stopped on distinct window %d: %s", i+1, d.reason)
			}
		}
	}
}

type failingSessionLog struct {
	session.Memory
	failType string
	keep     bool
}

func (l *failingSessionLog) Append(e session.Event) (int, error) {
	if e.Type == l.failType {
		seq := 0
		if l.keep {
			seq, _ = l.Memory.Append(e)
		}
		return seq, errors.New("disk full")
	}
	return l.Memory.Append(e)
}

func TestPersistenceFailureStopsBeforeMutationAndRetainsEvidence(t *testing.T) {
	for _, kind := range []string{session.TypeToolCall, session.TypeToolResult} {
		t.Run(kind, func(t *testing.T) {
			f := &fake{responses: []*llm.Response{
				toolCall("w", "write_file", `{"path":"a.txt","content":"changed"}`),
				toolCall("w2", "write_file", `{"path":"a.txt","content":"second"}`),
			}}
			a, root := newAgent(t, f, nil)
			a.AutoApprove = true
			a.Log = &failingSessionLog{failType: kind}
			err := a.Run(context.Background(), "make changes", nil)
			if !errors.Is(err, ErrSessionLog) || !strings.Contains(err.Error(), "disk full") {
				t.Fatalf("err = %v", err)
			}
			got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			want := "alpha\n"
			if kind == session.TypeToolResult {
				want = "changed"
			}
			if string(got) != want {
				t.Fatalf("file = %q, want %q", got, want)
			}
			if len(f.requests) != 1 {
				t.Fatalf("made %d model calls after persistence failed", len(f.requests))
			}
			found := false
			for _, e := range a.logEvents() {
				if e.Type == kind {
					found = true
				}
			}
			if !found {
				t.Fatal("lost the failed event from memory")
			}
		})
	}
}

func TestPersistenceSyncFailureDoesNotDuplicateEvent(t *testing.T) {
	a := &Agent{Log: &failingSessionLog{failType: session.TypeNotice, keep: true}}
	a.logEvent(session.Event{Type: session.TypeNotice, Output: "one"})
	a.logEvent(session.Event{Type: session.TypeNotice, Output: "two"})
	evs := a.logEvents()
	if len(evs) != 2 || evs[0].Output != "one" || evs[1].Output != "two" || evs[1].Seq <= evs[0].Seq {
		t.Fatalf("events = %+v", evs)
	}
	a.SetLog(&session.Memory{})
	if err := a.persistenceErr(); err != nil {
		t.Fatalf("new sink retained old failure: %v", err)
	}
}

func TestWrapStopRunsMandatoryVerification(t *testing.T) {
	for _, command := range []string{"echo checked > marker", "echo checked > marker; exit 1"} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			a := &Agent{Log: &session.Memory{}, Root: root}
			a.mutations = 1
			a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{command}}}
			err := a.stopForWrap(context.Background(), "old passing check", func(Event) {})
			if _, statErr := os.Stat(filepath.Join(root, "marker")); statErr != nil {
				t.Fatal("verification did not run")
			}
			if strings.Contains(command, "exit 1") && !errors.Is(err, ErrVerification) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestWrapStopRunsLockedAcceptanceGate(t *testing.T) {
	root := t.TempDir()
	a := &Agent{Log: &session.Memory{}, Root: root}
	a.mutations = 1
	a.SetAccept(&session.AcceptMeta{Phase: session.AcceptLocked, Root: root, Command: "echo checked > marker; exit 1"}, nil)
	err := a.stopForWrap(context.Background(), "old passing check", func(Event) {})
	if !errors.Is(err, ErrVerification) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); err != nil {
		t.Fatal("locked acceptance gate was bypassed")
	}
}

func TestParallelChildrenShareOneRemainingBudget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &spendyChild{}
	parent := &Agent{Provider: p, Model: "m"}
	parent.MaxCostUSD = 1
	task := &Task{Parent: parent}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		child := &Agent{Provider: p, Model: "m", Tools: tool.NewRegistry(tool.ReadFile{Root: root})}
		task.inheritBudget(child)
		go func() { results <- child.Run(context.Background(), "read repeatedly", nil) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; !errors.Is(err, ErrBudget) {
			t.Fatalf("child error = %v", err)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.childCalls != 3 {
		t.Fatalf("made %d calls at $0.40 under a shared $1 ceiling, want 3", p.childCalls)
	}
	if spent, over := parent.overBudget(); !over || spent < 1.19 || spent > 1.21 {
		t.Fatalf("spent=%f over=%v", spent, over)
	}
}

func TestCheapRoutingCannotExecuteEdits(t *testing.T) {
	for _, prose := range []bool{false, true} {
		t.Run(fmt.Sprint(prose), func(t *testing.T) {
			main := &fake{responses: []*llm.Response{
				toolCall("r", "read_file", `{"path":"a.txt"}`), text("inspection finished"),
			}}
			r := costly(0.25, "w", "write_file", `{"path":"a.txt","content":"cheap edit"}`)
			if prose {
				r.Message = llm.TextMessage(llm.RoleAssistant, `[tool_use]{"name":"write_file","input":{"path":"a.txt","content":"cheap edit"}}`)
				r.StopReason = llm.StopEndTurn
			}
			cheap := &fake{responses: []*llm.Response{r}}
			a, root := newAgent(t, main, nil)
			a.AutoApprove = true
			a.RouteCheap = "cheap"
			a.Route = func(model string) (llm.Provider, string, error) { return cheap, model, nil }
			if err := a.Run(context.Background(), "inspect a.txt", nil); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			if string(got) != "alpha\n" {
				t.Fatalf("cheap model edited file: %q", got)
			}
			if len(main.requests) != 2 || len(cheap.requests) != 1 {
				t.Fatalf("main=%d cheap=%d", len(main.requests), len(cheap.requests))
			}
			for _, d := range cheap.requests[0].Tools {
				if !mechanicalTools[d.Name] {
					t.Fatalf("cheap model received %s", d.Name)
				}
			}
			if cost, _, _, ok := a.SessionCost(); !ok || cost != .25 {
				t.Fatalf("discarded cheap usage not billed: cost=%f known=%v", cost, ok)
			}
		})
	}
}

func TestBudgetReseedsWhenSessionChanges(t *testing.T) {
	f := &fake{responses: []*llm.Response{costly(.4, "r", "read_file", `{"path":"a.txt"}`), text("done")}}
	a, _ := newAgent(t, f, nil)
	a.SetMaxCost(.5)
	a.SetLog(&session.Memory{})
	if err := a.Run(context.Background(), "read", nil); err != nil {
		t.Fatal(err)
	}
	a.SetLog(&session.Memory{})
	a.Provider = &fake{responses: []*llm.Response{costly(.4, "r2", "read_file", `{"path":"a.txt"}`), text("done")}}
	if err := a.Run(context.Background(), "read again", nil); err != nil {
		t.Fatalf("old session consumed the new budget: %v", err)
	}
}

func TestBudgetIncludesCancelledStreamingUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, _ := newAgent(t, &fake{}, nil)
	a.Provider = &streamThenCancel{cancel: cancel, text: strings.Repeat("output ", 100)}
	a.Price = &llm.Price{Input: 1, Output: 1}
	a.SetMaxCost(1)
	if err := a.Run(ctx, "explain", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if spent, over := a.overBudget(); spent <= 0 || over {
		t.Fatalf("cancelled stream not charged correctly: spent=%f over=%v", spent, over)
	}
}

func TestWrapStopIntegrityFailureInvalidatesGreenVerdict(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "a_test.go")
	if err := os.WriteFile(locked, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{Log: &session.Memory{}, Root: root}
	a.turnN, a.mutations = 1, 1
	a.logEvent(session.Event{Type: session.TypeToolResult, CallID: "w", Name: "write_file", Arguments: `{"path":"a.go"}`, Output: "Created a.go"})
	a.logEvent(session.Event{Type: session.TypeToolResult, CallID: "t", Name: "bash", Arguments: `{"command":"go test ./..."}`, Output: "ok a 0.001s"})
	if !a.Outcome().OK() {
		t.Fatalf("fixture is not green: %+v", a.Outcome())
	}
	a.SetAccept(&session.AcceptMeta{Phase: session.AcceptLocked, Root: root, Files: []string{locked}, Hashes: map[string]string{locked: "wrong"}, Command: "echo unsafe > marker"}, nil)
	evs, emit := collect()
	if err := a.stopForWrap(context.Background(), "go test ./...", emit); !errors.Is(err, ErrVerification) {
		t.Fatalf("err = %v", err)
	}
	for _, e := range *evs {
		if e.Kind == EvVerdict && !e.IsError {
			t.Fatal("integrity failure emitted a green verdict")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
		t.Fatal("ran acceptance command with compromised tests")
	}
}
