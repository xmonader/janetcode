package agent

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// holdForkProbe starts a background fork on the parent's first call and
// holds the fork's model call open until its context ends.
type holdForkProbe struct {
	started   chan struct{}
	cancelled atomic.Bool
}

func (h *holdForkProbe) Name() string { return "fake" }
func (h *holdForkProbe) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	switch req.Messages[len(req.Messages)-1].Text() {
	case "go":
		return toolCall("f1", "task", `{"description":"bg","prompt":"do it","fork":true}`), nil
	case "do it":
		close(h.started)
		<-ctx.Done()
		h.cancelled.Store(true)
		return nil, ctx.Err()
	}
	return text("ok"), nil
}

func newLoggedSession(t *testing.T, dir string) *session.Session {
	t.Helper()
	s, err := session.New(dir, "/work", "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	return s
}

// A fork started in session A is still running when the conversation moves
// to session B (/new, /resume). It must end with A: stopped while the log
// still points at A, its end recorded in A's attempt record, and nothing
// of it written into B or left running to write there later.
func TestSubagentOfTheSessionLeftDoesNotFinishIntoTheNextOne(t *testing.T) {
	for name, move := range map[string]func(*Agent, *session.Session) error{
		"new or resume": func(a *Agent, b *session.Session) error { return a.OpenSession(b, nil) },
		"fork":          func(a *Agent, b *session.Session) error { return a.ForkSession(b) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			a, b := newLoggedSession(t, dir), newLoggedSession(t, dir)
			prov := &holdForkProbe{started: make(chan struct{})}
			parent := &Agent{Provider: prov, Model: "m", System: "sys", AutoApprove: true, Log: a, Root: dir}
			parent.Attempts = OpenAttempts(filepath.Join(dir, "a.attempts.log"))
			task := &Task{Parent: parent}
			parent.Tools = tool.NewRegistry(tool.ReadFile{Root: dir}, task)
			if err := parent.Run(context.Background(), "go", func(Event) {}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-prov.started:
			case <-time.After(5 * time.Second):
				t.Fatal("the fork never started")
			}
			if len(task.ChildIDs()) == 0 {
				t.Fatal("no continuable child before the switch")
			}
			bBefore := len(b.Events())

			done := make(chan error, 1)
			go func() { done <- move(parent, b) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("the session switch hung on a running subagent")
			}

			if !prov.cancelled.Load() {
				t.Fatal("the subagent was left running across the switch")
			}
			if !task.Wait(context.Background()) {
				t.Fatal("subagent goroutines outlived the switch")
			}
			if ids := task.ChildIDs(); len(ids) != 0 {
				t.Fatalf("the new session inherited the old one's subagents: %v", ids)
			}
			atts := parent.Attempts.All()
			if len(atts) != 1 || atts[0].Outcome != OutcomeCancelled {
				t.Fatalf("A's attempt record = %+v, want one cancelled attempt", atts)
			}
			for _, e := range b.Events()[bBefore:] {
				if e.Name == "subagent/merge" || e.Type == session.TypeUsage {
					t.Fatalf("the old session's subagent wrote %s into the new session", e.Type)
				}
			}
		})
	}
}
