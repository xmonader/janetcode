package anthropic

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// okStream is a complete, minimal successful response.
func okStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
	io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`))
	io.WriteString(w, event("content_block_stop", `{"type":"content_block_stop","index":0}`))
	io.WriteString(w, event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`))
	io.WriteString(w, event("message_stop", `{"type":"message_stop"}`))
}

func hi() llm.Request {
	return llm.Request{Model: "m", MaxTokens: 10, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
}

// The retry is the harness's, not the SDK's: it counts down where the
// user can see it, waits as long as the server asked, and leaves a
// record of itself once the call finally lands.
func TestRateLimitIsRetriedByTheHarness(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		okStream(w)
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	var status, retried int
	var got *llm.Retry
	start := time.Now()
	resp, err := p.Stream(context.Background(), hi(), func(e llm.Event) {
		switch e.Type {
		case llm.EventStatus:
			status++
		case llm.EventRetried:
			retried++
			got = e.Retry
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "ok" {
		t.Fatalf("text = %q", resp.Message.Text())
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("%d requests, want the refusal and the retry", n)
	}
	if status != 1 || retried != 1 {
		t.Fatalf("%d status events and %d retried events, want 1 and 1", status, retried)
	}
	if got == nil || got.Attempts != 1 || got.Reason != "rate limited" || got.Wait <= 0 {
		t.Fatalf("retried event = %+v", got)
	}
	if waited := time.Since(start); waited < 900*time.Millisecond {
		t.Fatalf("waited %v, want the second the server asked for", waited)
	}
}

// A plain 500 is the server saying this request broke it. The SDK would
// have retried it behind the harness's back; the harness does not.
func TestInternalErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	_, err = p.Stream(context.Background(), hi(), func(e llm.Event) {
		if e.Type == llm.EventStatus || e.Type == llm.EventRetried {
			seen++
		}
	})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 500 {
		t.Fatalf("err = %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
	if seen != 0 {
		t.Fatalf("%d retry events on a failure that was never retried", seen)
	}
}

// The count budget (rate limits, as gateway outages spend a time budget
// instead) is the one the caller configured, and it is spent once.
func TestRetriesStopAtTheBudget(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stream(context.Background(), hi(), nil); err == nil {
		t.Fatal("want an error")
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("%d requests, want the first plus two retries", n)
	}
}

// A backoff the harness imposed on itself is not a stalled stream: the
// watchdog is told to expect it, so the idle bound does not fire while
// the harness is deliberately waiting.
func TestBackoffDoesNotTripTheIdleWatchdog(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		okStream(w)
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 2, Limits: llm.StreamLimits{Idle: 400 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Stream(context.Background(), hi(), nil)
	if err != nil {
		t.Fatalf("the idle bound fired during the backoff: %v", err)
	}
	if resp.Message.Text() != "ok" {
		t.Fatalf("text = %q", resp.Message.Text())
	}
}

// A user who interrupts is not made to sit through the backoff.
func TestCancellationEndsTheBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := p.Stream(ctx, hi(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("waited %v after cancellation", waited)
	}
}

// An error event inside a 200 stream is the server's status arriving
// late: overloaded_error is Anthropic's 529, sent after message_start
// and before a single token. It must read and retry as a 529, not as an
// "Overloaded (HTTP 200)" that is final because the status says success
// and message_start counted as the model having said something.
func TestOverloadedEventBeforeAnyContentIsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
			io.WriteString(w, event("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		}
		okStream(w)
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	var discarded bool
	resp, err := p.Stream(context.Background(), hi(), func(e llm.Event) {
		if e.Type == llm.EventStreamDiscard {
			discarded = true
		}
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 2 || resp.Message.Text() != "ok" {
		t.Fatalf("calls = %d text = %q", calls.Load(), resp.Message.Text())
	}
	if discarded {
		t.Fatal("nothing was emitted, so there was nothing to discard")
	}
}

// Once the model has spoken, the same event stays final, but it names
// the real status rather than the 200 the stream opened with.
func TestOverloadedEventAfterContentNamesItsStatus(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
		io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"par"}}`))
		io.WriteString(w, event("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	}))
	defer srv.Close()
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL, MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Stream(context.Background(), hi(), nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 529 || calls.Load() != 1 {
		t.Fatalf("err = %v calls = %d", err, calls.Load())
	}
}
