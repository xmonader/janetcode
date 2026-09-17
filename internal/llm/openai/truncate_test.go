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
