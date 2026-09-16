package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// A plain 500 is the server saying this request broke it; sending the
// same request again breaks it again, with the real reason buried under
// the retries.
func TestInternalErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"internal error"}}`, 500)
	})
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
	for _, status := range []int{502, 503, 504, 429, 408, 529} {
		if !retryable(&APIError{Status: status, Message: "x"}) {
			t.Errorf("%d should be retried", status)
		}
	}
	for _, status := range []int{500, 501, 400, 404, 422} {
		if retryable(&APIError{Status: status, Message: "x"}) {
			t.Errorf("%d should not be retried", status)
		}
	}
}

func TestRetryAfterIsHonouredOnlyWhereItMeansComeBack(t *testing.T) {
	if got := retryAfter("2"); got != 2*time.Second {
		t.Fatalf("seconds form = %v", got)
	}
	if got := retryAfter(time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)); got <= 0 || got > 4*time.Second {
		t.Fatalf("date form = %v", got)
	}
	if got := retryAfter("3600"); got != 0 {
		t.Fatalf("an hour should be ignored, got %v", got)
	}
	for _, v := range []string{"", "soon", "-1", "0"} {
		if got := retryAfter(v); got != 0 {
			t.Fatalf("retryAfter(%q) = %v", v, got)
		}
	}

	// The header is kept on 429 and 503, and nowhere else: on another
	// status it is advice about a failure that is not going to clear.
	for _, tc := range []struct {
		status int
		want   time.Duration
	}{{429, time.Second}, {503, time.Second}, {500, 0}, {400, 0}} {
		status := tc.status
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, fmt.Sprintf(`{"error":{"message":"status %d"}}`, status), status)
		}))
		// -1, not 0: a zero MaxRetries is the default of two, and this
		// case is about the header on the error, not about retrying.
		p, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k", MaxRetries: -1})
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
		var ae *APIError
		if !errors.As(err, &ae) {
			t.Fatalf("status %d: err = %v", status, err)
		}
		if ae.RetryAfter != tc.want {
			t.Errorf("status %d: RetryAfter = %v, want %v", status, ae.RetryAfter, tc.want)
		}
		srv.Close()
	}
}

// A 429 with a Retry-After waits for as long as the server asked, not
// for the harness's guess.
func TestRetryWaitsAsTheServerAsked(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":{"message":"slow down"}}`, 429)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	p, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k", MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "ok" {
		t.Fatalf("text = %q", resp.Message.Text())
	}
	if waited := time.Since(start); waited < 900*time.Millisecond {
		t.Fatalf("waited %v, want the second the server asked for", waited)
	}
}

// A retry that succeeds leaves a record of itself. The status line that
// counted it down is gone the moment the call lands, so without this a
// turn that took three attempts is indistinguishable from a clean one.
func TestSucceededRetryIsReported(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, `{"error":{"message":"slow down"}}`, 429)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	p, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k", MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	var got *llm.Retry
	_, err = p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, func(e llm.Event) {
		if e.Type == llm.EventRetried {
			got = e.Retry
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("no retried event after two refusals")
	}
	if got.Attempts != 2 || got.Reason == "" || got.Wait <= 0 {
		t.Fatalf("retry = %+v, want two attempts with a reason and a wait", *got)
	}
}

// A call that never failed says nothing.
func TestCleanCallReportsNoRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	p, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k", MaxRetries: 3})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, func(e llm.Event) {
		if e.Type == llm.EventRetried {
			seen = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	if seen {
		t.Fatal("a clean call reported a retry")
	}
}

// A zero MaxRetries is the default of two (TestHTTPErrorAndRetry covers
// that); a negative one is how a caller says "do not retry at all",
// which a plain 0 could not express.
func TestNegativeMaxRetriesDisablesRetrying(t *testing.T) {
	var calls atomic.Int32
	p := newTestProviderWith(t, Config{MaxRetries: -1}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
	})
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil); err == nil {
		t.Fatal("want an error")
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}

// A retry backoff is a wait the harness chose. Without telling the
// watchdog so, a backoff longer than the idle bound cancelled the call
// in the middle of the wait and reported a stall that never happened.
func TestRetryBackoffDoesNotTripTheIdleWatchdog(t *testing.T) {
	var calls atomic.Int32
	cfg := Config{
		MaxRetries: 2,
		// Shorter than the 500ms first backoff, so the old behaviour
		// fired during the wait; the lifetime bound stays generous.
		Limits: llm.StreamLimits{Idle: 150 * time.Millisecond, Max: 30 * time.Second},
	}
	p := newTestProviderWith(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, `{"error":{"message":"slow down"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatalf("the backoff was read as a stall: %v", err)
	}
	if resp.Message.Text() != "ok" {
		t.Fatalf("text = %q", resp.Message.Text())
	}
}
