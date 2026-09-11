package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// One task walking into a rate limit used to put a gap in front of every
// other task in the process, because they all shared the provider's
// pacer. The eval driver runs its tasks concurrently in one process, so a
// single 429 slowed the whole run down — the same tasks in separate
// processes did not pay it.
func TestARefusalPacesOnlyTheTaskThatWasRefused(t *testing.T) {
	var refused atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refused.CompareAndSwap(false, true) {
			http.Error(w, `{"error":{"message":"slow down"}}`, 429)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	base, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k", MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	one, two := base.ForTask().(*Provider), base.ForTask().(*Provider)
	req := llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
	if _, err := one.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if one.pace.Gap() <= 0 {
		t.Fatal("the refused task is not pacing itself")
	}
	if gap := two.pace.Gap(); gap != 0 {
		t.Fatalf("the other task is paced at %v by a refusal that was not its own", gap)
	}
	if gap := base.pace.Gap(); gap != 0 {
		t.Fatalf("the parent is paced at %v", gap)
	}
	if _, err := two.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
}

// The key ring is the opposite case: a key that is out of quota is out of
// quota for everyone, so siblings share it and the second task never
// spends a request finding that out again.
func TestSiblingsShareTheKeyRingAndTheDialect(t *testing.T) {
	var seen atomic.Int32
	var sawMaxCompletion atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"max_completion_tokens"`) {
			sawMaxCompletion.Add(1)
		} else if seen.Add(1) == 1 {
			http.Error(w, `{"error":{"message":"use max_completion_tokens instead of max_tokens"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	base, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	one, two := base.ForTask().(*Provider), base.ForTask().(*Provider)
	if one.ring != base.ring || two.ring != base.ring {
		t.Fatal("a sibling has a key ring of its own")
	}
	req := llm.Request{Model: "m", MaxTokens: 16, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
	if _, err := one.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := two.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	// Two calls sent max_completion_tokens: the resend on the first task
	// and the whole of the second, which never had to be told.
	if n := sawMaxCompletion.Load(); n != 2 {
		t.Fatalf("%d calls used the learned dialect, want 2", n)
	}
	if n := seen.Load(); n != 1 {
		t.Fatalf("the dialect was learned %d times, want once", n)
	}
}

// ForTaskOr leaves a provider that has nothing to hand out alone.
func TestForTaskOrPassesThroughAPlainProvider(t *testing.T) {
	p := plainProvider{}
	if got := llm.ForTaskOr(p); got != llm.Provider(p) {
		t.Fatal("a plain provider was replaced")
	}
}

type plainProvider struct{}

func (plainProvider) Name() string { return "plain" }
func (plainProvider) Stream(context.Context, llm.Request, func(llm.Event)) (*llm.Response, error) {
	return &llm.Response{}, nil
}
