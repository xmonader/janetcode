package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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
	if notes := task.FinishedForks(); len(notes) != 1 || strings.Contains(notes[0], "error") {
		t.Fatalf("fork report = %v", notes)
	}
}
