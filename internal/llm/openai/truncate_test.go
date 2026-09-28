package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// A stream that dies partway through never produced an answer, so the
// whole request is retried as one fresh attempt over the intact history;
// the partial deltas were only ever events, and the retried response
// replaces them.
func TestTruncatedStreamIsRetried(t *testing.T) {
	var calls int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomicAdd(&calls)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// One real frame, then the connection dies — no [DONE], so the
			// attempt has no terminal frame to its name.
			io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"half an"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // connection dies mid-answer
		}
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"full answer"},"finish_reason":"stop"}]}`))
	})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 || resp.Message.Text() != "full answer" {
		t.Fatalf("calls = %d text = %q", calls, resp.Message.Text())
	}
}

// A server that spoke mid-stream — a structured error frame after real
// deltas — is answering, not truncating: the failure is final.
func TestMidStreamErrorFrameIsFinal(t *testing.T) {
	var calls int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		atomicAdd(&calls)
		io.WriteString(w, sse(
			`{"choices":[{"delta":{"content":"so far"}}]}`,
			`{"error":{"message":"content policy"}}`,
		))
	})
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err == nil || !strings.Contains(err.Error(), "content policy") || calls != 1 {
		t.Fatalf("err = %v calls = %d", err, calls)
	}
}

// A stream the watchdog killed for silence is retried under the fresh
// watchdog the next attempt gets: the kill is the harness giving up on
// one attempt, not the user giving up on the turn.
func TestWatchdogStallIsRetriedUnderAFreshWatchdog(t *testing.T) {
	var calls int32
	p := newTestProviderWith(t, Config{MaxRetries: 1, Limits: llm.StreamLimits{Idle: 80 * time.Millisecond}},
		func(w http.ResponseWriter, r *http.Request) {
			n := atomicAdd(&calls)
			w.Header().Set("Content-Type", "text/event-stream")
			if n == 1 {
				// One frame, then silence — no [DONE], so the attempt has
				// no terminal frame when the watchdog ends it. Blocking on
				// the request context lets the handler return when the
				// client goes away.
				io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"first"}}]}`+"\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			io.WriteString(w, sse(`{"choices":[{"delta":{"content":"second"},"finish_reason":"stop"}]}`))
		})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 || resp.Message.Text() != "second" {
		t.Fatalf("calls = %d text = %q", calls, resp.Message.Text())
	}
}

// The user's own cancellation is final — the turn is dead because they
// killed it, not because the server did.
func TestUserCancelIsFinal(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"x"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // hold the stream open until the client leaves
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := p.Stream(ctx, llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil); err == nil {
		t.Fatal("expected an error")
	}
}

func atomicAdd(n *int32) int32 {
	// The test handlers are called serially; a plain add is fine.
	*n++
	return *n
}

// TestLifetimeCapEndsTheCall: the lifetime cap is the bound on one
// streamed call — the thing that ends a stream dribbling a token a minute
// forever. Building the watchdog inside the retry loop restarted it per
// attempt and treated its kill as a dead connection, so a 900s cap became
// three attempts of 900s and three generations billed for one answer.
func TestLifetimeCapEndsTheCall(t *testing.T) {
	var calls int32
	p := newTestProviderWith(t, Config{
		MaxRetries: 2,
		Limits:     llm.StreamLimits{Idle: -1, Max: 150 * time.Millisecond},
	}, func(w http.ResponseWriter, r *http.Request) {
		atomicAdd(&calls)
		w.Header().Set("Content-Type", "text/event-stream")
		// Keeps talking, so the idle bound would never fire: exactly the
		// stream the lifetime cap is for.
		for {
			if _, err := io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"x"}}]}`+"\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
	start := time.Now()
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "stream_max_duration") {
		t.Fatalf("err = %v, want the lifetime cap", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1: a lifetime cap is not a dead connection to retry", calls)
	}
	// The whole call, retries included, stays inside the one budget.
	if elapsed > time.Second {
		t.Fatalf("the call took %s against a 150ms cap", elapsed)
	}
}

// TestLifetimeBudgetSpansRetries: even where a retry is right — a stream
// that died without a terminal frame — the attempts share one lifetime
// budget, because it bounds the call and not the attempt.
func TestLifetimeBudgetSpansRetries(t *testing.T) {
	var calls int32
	p := newTestProviderWith(t, Config{
		MaxRetries: 5,
		Limits:     llm.StreamLimits{Idle: -1, Max: 200 * time.Millisecond},
	}, func(w http.ResponseWriter, r *http.Request) {
		atomicAdd(&calls)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"half"}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(120 * time.Millisecond)
		panic(http.ErrAbortHandler) // dies with no terminal frame, every time
	})
	start := time.Now()
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the retries took %s: the budget restarted per attempt", elapsed)
	}
	if calls > 3 {
		t.Fatalf("calls = %d: each attempt got a fresh lifetime budget", calls)
	}
}

// TestTruncatedRetryEmitsTheDiscard: the retry is only safe because
// consumers are told the partial deltas are void. Nothing asserted the
// event was emitted at all, so a consumer that ignored it looked correct.
func TestTruncatedRetryEmitsTheDiscard(t *testing.T) {
	var calls int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomicAdd(&calls)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"half an"}}]}`+"\n\n")
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"full answer"},"finish_reason":"stop"}]}`))
	})
	var text string
	var discards int
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, func(e llm.Event) {
		switch e.Type {
		case llm.EventTextDelta:
			text += e.Text
		case llm.EventStreamDiscard:
			discards++
			text = ""
		}
	}); err != nil {
		t.Fatal(err)
	}
	if discards != 1 {
		t.Fatalf("discards = %d, want 1", discards)
	}
	if text != "full answer" {
		t.Fatalf("a consumer that honours the discard read %q", text)
	}
}

// A server that goes silent before it has even sent the response headers
// is the same stall as one that goes silent mid-stream, and is retried
// the same way. The kill used to surface inside the request loop as the
// transport's bare context cancellation, which the retry policy reads as
// the user giving up — so it was final here while the Anthropic provider
// and a mid-stream stall were both retried.
func TestStallBeforeHeadersIsRetried(t *testing.T) {
	var calls int32
	p := newTestProviderWith(t, Config{MaxRetries: 1, Limits: llm.StreamLimits{Idle: 80 * time.Millisecond}},
		func(w http.ResponseWriter, r *http.Request) {
			if atomicAdd(&calls) == 1 {
				// Read the body first: only then does the server watch the
				// connection and cancel r.Context when the client leaves.
				io.Copy(io.Discard, r.Body)
				<-r.Context().Done() // never answers
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`{"choices":[{"delta":{"content":"second"},"finish_reason":"stop"}]}`))
		})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 || resp.Message.Text() != "second" {
		t.Fatalf("calls = %d text = %q", calls, resp.Message.Text())
	}
}
