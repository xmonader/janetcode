package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// forkProbe asks for a fork on the parent's first call and records every
// request, the fork's included.
type forkProbe struct {
	mu   sync.Mutex
	reqs [][]llm.Message
}

func (f *forkProbe) Name() string { return "fake" }
func (f *forkProbe) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, copyMessages(req.Messages))
	f.mu.Unlock()
	if req.Messages[len(req.Messages)-1].Text() == "go" {
		return toolCall("f1", "task", `{"description":"bg","prompt":"do it","fork":true}`), nil
	}
	return text("ok"), nil
}

// The editing child and the fork were given the parent's context window;
// the read-only child was not, so its compaction point and its guard
// against oversized tool output were not fitted to the model.
func TestEveryKindOfChildKnowsTheContextWindow(t *testing.T) {
	parent := &Agent{Provider: &inFlight{}, Model: "m", System: "sys"}
	parent.ContextWindow = 32_000
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}, task)
	ro, err := task.child(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fork, err := task.forkChild()
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]*Agent{"read-only": ro, "fork": fork} {
		if w := c.Settings().ContextWindow; w != 32_000 {
			t.Errorf("%s child has context window %d, want the parent's 32000", name, w)
		}
	}
}

// inFlight answers every call after a short wait and remembers the most
// calls it ever had open at once.
type inFlight struct {
	mu        sync.Mutex
	open, max int
}

func (f *inFlight) Name() string { return "fake" }
func (f *inFlight) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	f.mu.Lock()
	f.open++
	f.max = max(f.max, f.open)
	f.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	f.open--
	f.mu.Unlock()
	return text("report"), nil
}

// Two continues of one finished child, made in the same turn (task calls
// run in parallel), both passed the "still running" check: the child was
// only marked running after the start hook, so both drove the same agent
// at once, interleaving its history.
func TestTwoContinuesOfOneChildDoNotBothRunIt(t *testing.T) {
	f := &inFlight{}
	parent := &Agent{Provider: f, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}, task)
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"one","prompt":"look"}`)); err != nil {
		t.Fatal(err)
	}
	id := task.ChildIDs()[0]
	// A start hook that takes a moment, as a real command does.
	parent.Hooks = &hook.Runner{Cwd: t.TempDir(), Config: hook.Config{Hooks: hook.Hooks{
		hook.SubagentStart: {{Command: "sleep 0.3"}},
	}}}
	outs := make([]string, 2)
	var wg sync.WaitGroup
	for i := range outs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := task.Run(context.Background(), json.RawMessage(`{"prompt":"again","continue":"`+id+`"}`))
			if err != nil {
				t.Error(err)
			}
			outs[i] = out
		}()
	}
	wg.Wait()
	busy := 0
	for _, o := range outs {
		if strings.Contains(o, "still running") {
			busy++
		}
	}
	if busy != 1 {
		t.Fatalf("want exactly one continue turned away, got %d: %q", busy, outs)
	}
}

// A fork the model asks for starts inside the parent's tool step, when
// the task call is in the history and its result is not. The fork's
// first request carried that unanswered tool_use, which every provider
// refuses, so a model-spawned fork failed on its first call.
func TestAModelSpawnedForkSendsAPairedHistory(t *testing.T) {
	p := &forkProbe{}
	parent := &Agent{Provider: p, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}, task)
	if err := parent.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if !task.Wait(ctx) {
		t.Fatal("fork did not finish")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	forkSeen := false
	for _, r := range p.reqs {
		if r[len(r)-1].Text() == "do it" {
			forkSeen = true
		}
		assertPaired(t, r)
	}
	if !forkSeen {
		t.Fatal("the fork never called the model; the test proves nothing")
	}
	log, ok := task.ChildLog(task.ChildIDs()[0])
	if !ok || !log.Done || lastAssistantText(log.Messages) != "ok" {
		t.Fatalf("fork did not finish with its report: %+v", log)
	}
}
