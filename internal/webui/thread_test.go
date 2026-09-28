package webui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
)

// The thread was handed out as pointers: the agent's goroutine appended to
// an entry's text while an HTTP request was serialising the very same
// struct. Under -race this is the regression test for that; without it,
// it is at least a check that a reader sees consistent entries.
func TestReadingTheThreadWhileATurnWritesIt(t *testing.T) {
	s := newServer(context.Background(), Options{})
	const rounds = 300
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range rounds {
			s.apply(agent.Event{Kind: agent.EvText, Text: "word "})
			call := agent.ToolCall{ID: "t" + string(rune('a'+i%26)), Name: "bash", Input: json.RawMessage(`{"command":"ls -l"}`)}
			s.apply(agent.Event{Kind: agent.EvToolStart, Call: &call})
			s.apply(agent.Event{Kind: agent.EvToolInput, Call: &call})
			s.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
			s.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: "ok"})
			s.apply(agent.Event{Kind: agent.EvTextBlockDone})
		}
	}()
	for range rounds {
		_, ents, _, _, _ := s.snapshot()
		for _, e := range ents {
			_ = e.Text + e.Output + e.Summary
		}
		w := httptest.NewRecorder()
		s.state(w, httptest.NewRequest("GET", "/api/state", nil))
		if w.Code != 200 {
			t.Fatalf("/api/state = %d", w.Code)
		}
	}
	<-done

	// Every tool call ended up as one finished entry with its result.
	_, ents, _, _, _ := s.snapshot()
	tools := 0
	for _, e := range ents {
		if e.Kind != "tool" {
			continue
		}
		tools++
		if !e.Done || e.Output != "ok" {
			t.Fatalf("tool entry %+v was not closed out", e)
		}
		if e.Summary != "ls -l" {
			t.Fatalf("tool entry summary = %q", e.Summary)
		}
	}
	if tools != rounds {
		t.Fatalf("%d tool entries, want %d", tools, rounds)
	}
}

// finish used to empty the queue into a local and write the remainder back
// after the next turn had already been started, so anything the user typed
// in that window was overwritten and never sent.
func TestAPromptQueuedWhileATurnEndsIsNotLost(t *testing.T) {
	s, _ := planServer(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "one"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "two"), StopReason: llm.StopEndTurn},
	)
	s.mu.Lock()
	s.running = true
	s.queued = []string{"first", "second"}
	s.mu.Unlock()

	s.finish(nil) // the turn that was running ends

	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.running && len(s.queued) == 0
	})
	_, ents, _, _, _ := s.snapshot()
	var typed []string
	for _, e := range ents {
		if e.Kind == "user" {
			typed = append(typed, e.Text)
		}
	}
	if len(typed) != 2 || typed[0] != "first" || typed[1] != "second" {
		t.Fatalf("prompts sent = %q, want both queued prompts in order", typed)
	}
}

// A turn that ends with a prompt queued hands the session straight to it.
// It used to be marked idle first and the queued turn begun afterwards: a
// prompt sent in that gap saw an idle session and started ahead, the
// queued prompt's begin then failed, and it was dropped without a word.
func TestTheQueuedPromptKeepsTheSessionWhenATurnEnds(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.mu.Lock()
	s.running = true
	s.queued = []string{"queued"}
	s.mu.Unlock()

	next, ctx, _ := s.end(false)
	if next != "queued" || ctx == nil {
		t.Fatalf("end handed back %q with ctx %v", next, ctx)
	}
	if _, ok := s.begin("thinking"); ok {
		t.Fatal("something else began in the gap before the queued prompt's turn")
	}
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	cancel()
	if ctx.Err() == nil {
		t.Fatal("esc does not reach the queued prompt's turn")
	}
}
