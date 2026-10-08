package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// gatedDiagnose answers a check only when the test releases it, so where
// its note lands does not depend on scheduling.
type gatedDiagnose struct {
	mu      sync.Mutex
	release chan struct{}
	note    func(path string) string
	asked   []string
}

func (g *gatedDiagnose) diagnose(_ context.Context, path string) string {
	g.mu.Lock()
	g.asked = append(g.asked, path)
	g.mu.Unlock()
	<-g.release
	return g.note(path)
}

func diagLastText(req llm.Request) string {
	m := req.Messages[len(req.Messages)-1]
	var b strings.Builder
	for _, bl := range m.Blocks {
		b.WriteString(bl.Text)
	}
	return b.String()
}

// The edit returns without waiting for the language server; the finding
// joins the results of the next step that finds the check finished.
func TestDiagnosticsRunBesideTheTurn(t *testing.T) {
	g := &gatedDiagnose{release: make(chan struct{}), note: func(p string) string {
		return "[diagnostics]\n" + p + ":1:1: warning: unused"
	}}
	// tick lets the check finish, from inside the second step.
	tick := funcTool{name: "tick", run: func(json.RawMessage) (string, error) {
		close(g.release)
		time.Sleep(100 * time.Millisecond)
		return "ticked", nil
	}}
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.go","content":"package x"}`),
		toolCall("t2", "tick", `{}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Diagnose = g.diagnose
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tick)
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got := diagLastText(f.requests[1]); strings.Contains(got, "unused") || !strings.Contains(got, "Created x.go") {
		t.Fatalf("the edit's own result: %q", got)
	}
	if got := diagLastText(f.requests[2]); !strings.Contains(got, "ticked") || !strings.Contains(got, "x.go:1:1: warning: unused") {
		t.Fatalf("the next step's results: %q", got)
	}
}

// A check still running when the turn ends is waited for only so long,
// and a warning does not hold the turn.
func TestSlowDiagnosticsDoNotHoldTheTurn(t *testing.T) {
	old := diagWait
	diagWait = 50 * time.Millisecond
	defer func() { diagWait = old }()
	g := &gatedDiagnose{release: make(chan struct{}), note: func(string) string { return "" }}
	defer close(g.release)
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.go","content":"package x"}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Diagnose = g.diagnose
	start := time.Now()
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || time.Since(start) > 5*time.Second {
		t.Fatalf("model calls = %d after %s", len(f.requests), time.Since(start))
	}
}

// Errors a check finds after the model said it was done send it back, and
// a clean result after the fix ends the turn.
func TestDiagnosticErrorsAtTheEndSendTheModelBack(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	note := func(p string) string {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			// Still running when the step's results go out, so the
			// model first hears of it at the end of the turn.
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			mu.Lock()
			return "[diagnostics]\n" + p + ":3:1: error: undefined: y"
		}
		return "[diagnostics: no problems (gopls)]"
	}
	release := make(chan struct{})
	close(release)
	g := &gatedDiagnose{release: release, note: note}
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.go","content":"package x\nvar _ = y"}`),
		text("done"),
		toolCall("t2", "write_file", `{"path":"x.go","content":"package x"}`),
		text("fixed"),
	}}
	a, _ := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Diagnose = g.diagnose
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 4 {
		t.Fatalf("model calls = %d, want 4", len(f.requests))
	}
	var sentBack bool
	for _, r := range f.requests[1:3] {
		if strings.Contains(diagLastText(r), "x.go:3:1: error: undefined: y") {
			sentBack = true
		}
	}
	if !sentBack {
		t.Fatal("the error never reached the model")
	}
}

// With nothing but warnings, the turn ends when the model says so.
func TestDiagnosticWarningsDoNotHoldTheTurn(t *testing.T) {
	release := make(chan struct{})
	close(release)
	g := &gatedDiagnose{release: release, note: func(p string) string {
		return "[diagnostics]\n" + p + ":1:1: warning: unused"
	}}
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.go","content":"package x"}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Diagnose = g.diagnose
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(f.requests))
	}
}
