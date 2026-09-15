package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
)

// blockingProvider answers the first call with a scripted response and
// parks every call after it until its context is cancelled. That is what
// a fork wedged in a provider call looks like from the outside, which is
// the case close() has to survive.
type blockingProvider struct {
	first    *llm.Response
	calls    chan struct{} // one send per parked call
	release  chan struct{}
	done     chan struct{} // closed by the parked call when it returns
	doneOnce sync.Once
	mu       sync.Mutex
	n        int
}

// forkPrompt is the brief the fork is started with; a request carrying it
// as its last user message is the fork's own call, not the parent's.
const forkPrompt = "read everything"

func lastUserText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleUser {
			continue
		}
		for _, b := range msgs[i].Blocks {
			if b.Type == llm.BlockText {
				return b.Text
			}
		}
		return ""
	}
	return ""
}

func (p *blockingProvider) Name() string { return "fake" }

func (p *blockingProvider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	if lastUserText(req.Messages) != forkPrompt {
		// The parent: delegate once, then end the turn.
		p.mu.Lock()
		first := p.n == 0
		p.n++
		p.mu.Unlock()
		if first {
			for _, b := range p.first.Message.Blocks {
				bb := b
				emit(llm.Event{Type: llm.EventToolUseStart, Block: &bb})
				emit(llm.Event{Type: llm.EventToolUseEnd, Block: &bb})
			}
			return p.first, nil
		}
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "delegated"), StopReason: llm.StopEndTurn}, nil
	}
	// The fork, parked until its context is cancelled.
	select {
	case p.calls <- struct{}{}:
	default:
	}
	defer p.doneOnce.Do(func() {
		// Winding down takes a moment, the way a real call does. Close
		// must not report the session shut down before this has landed.
		time.Sleep(300 * time.Millisecond)
		close(p.done)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return nil, errors.New("released")
	}
}

// TestBuiltAgentCloseWaitsForSubagentAndCron is the shutdown contract of
// the composition root: a session that has a background fork in flight
// and a scheduled loop armed must come down on its own, promptly, and
// closing it twice must not do anything twice.
func TestBuiltAgentCloseWaitsForSubagentAndCron(t *testing.T) {
	cwd := t.TempDir()
	sessDir := t.TempDir()
	sess, err := session.New(sessDir, cwd, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	fork := &llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ID: "t1", Name: "task",
			Input: json.RawMessage(`{"description":"background reading","prompt":"read everything","fork":true}`),
		}}},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
	}
	prov := &blockingProvider{first: fork, calls: make(chan struct{}, 1), release: make(chan struct{}), done: make(chan struct{})}
	built, err := buildAgent(agentDeps{
		prov:    prov,
		cfg:     &config.Config{Model: "m", AutoApprove: true, MaxIterations: 2},
		cwd:     cwd,
		cfgDir:  t.TempDir(),
		sessDir: sessDir,
		mem:     memory.Open(t.TempDir(), cwd),
	}, sess, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A loop armed the way /loop arms one.
	if _, err := built.agent.Cron.Add(time.Second, "check the build"); err != nil {
		t.Fatal(err)
	}
	if len(built.agent.Cron.List()) != 1 {
		t.Fatalf("cron job not armed: %+v", built.agent.Cron.List())
	}

	// One turn, which starts the fork and returns without waiting for it.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = built.agent.Run(ctx, "delegate some reading", func(agent.Event) {})
	select {
	case <-prov.calls:
	case <-time.After(10 * time.Second):
		t.Fatal("the fork never reached the provider")
	}
	if kids := built.task.Children(); len(kids) != 1 || kids[0].Done {
		t.Fatalf("want one running fork, got %+v", kids)
	}

	// Closing cancels the fork, waits for it, and stops the scheduler.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		built.close()
		built.close() // twice: the root closes what it built, once.
	}()
	select {
	case <-closed:
	case <-time.After(shutdownGrace + 10*time.Second):
		t.Fatal("close did not return")
	}
	select {
	case <-prov.done:
	default:
		t.Fatal("close returned while the fork was still winding down: nothing waited for it")
	}
	if _, err := built.agent.Cron.Add(time.Second, "another"); err == nil {
		t.Fatal("the scheduler still takes jobs after close")
	}
	if kids := built.task.Children(); len(kids) != 0 {
		t.Fatalf("subagents survived close: %+v", kids)
	}
}
