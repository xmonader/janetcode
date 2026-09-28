package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
)

func postDecision(t *testing.T, h http.Handler, body map[string]string) int {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/permission", bytes.NewReader(b)))
	return rec.Code
}

// pendingID waits for a permission prompt to be up and returns its call id.
func pendingID(t *testing.T, s *server) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		p := s.pending
		s.mu.Unlock()
		if p != nil {
			return p.call.ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no permission prompt came up")
	return ""
}

// Two subagents that bubble their confirmations to the user ask at the
// same time. Each prompt must be answered on its own: the second used to
// overwrite the first, whose caller then waited for ever on a reply
// nothing could send, and the answer the user gave went to whichever call
// happened to be pending.
func TestConcurrentApprovalsAreEachAnswered(t *testing.T) {
	s := newServer(context.Background(), Options{Version: "test"})
	h := s.handler()
	type result struct {
		id string
		d  agent.Decision
	}
	got := make(chan result, 2)
	for _, id := range []string{"a", "b"} {
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			d, _, _, err := s.ApproveWithRule(ctx, agent.ToolCall{ID: id, Name: "bash"})
			if err != nil {
				t.Errorf("call %s: %v", id, err)
			}
			got <- result{id, d}
		}(id)
	}
	want := map[string]agent.Decision{}
	for i, dec := range []string{"allow", "deny"} {
		id := pendingID(t, s)
		if code := postDecision(t, h, map[string]string{"decision": dec, "id": id}); code != http.StatusOK {
			t.Fatalf("answer %d: %d", i, code)
		}
		want[id] = map[string]agent.Decision{"allow": agent.Allow, "deny": agent.Deny}[dec]
		select {
		case r := <-got:
			if r.id != id || r.d != want[id] {
				t.Fatalf("answered %s with %s, but %s returned %v", id, dec, r.id, r.d)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("call %s never got its answer", id)
		}
	}
}

// The page names the prompt it is answering. An answer to a prompt that
// is no longer the one pending — it was cancelled and another call is
// asking now — must not be taken as the answer to the new one.
func TestPermissionAnswerForAnotherPromptIsRefused(t *testing.T) {
	s := newServer(context.Background(), Options{Version: "test"})
	h := s.handler()
	done := make(chan agent.Decision, 1)
	go func() {
		d, _, _, _ := s.ApproveWithRule(context.Background(), agent.ToolCall{ID: "new", Name: "bash"})
		done <- d
	}()
	pendingID(t, s)
	if code := postDecision(t, h, map[string]string{"decision": "always", "id": "old"}); code != http.StatusConflict {
		t.Fatalf("stale answer: got %d, want 409", code)
	}
	select {
	case d := <-done:
		t.Fatalf("the stale answer decided the new prompt: %v", d)
	case <-time.After(50 * time.Millisecond):
	}
	if code := postDecision(t, h, map[string]string{"decision": "deny", "id": "new"}); code != http.StatusOK {
		t.Fatalf("answer: %d", code)
	}
	if d := <-done; d != agent.Deny {
		t.Fatalf("got %v, want deny", d)
	}
}
