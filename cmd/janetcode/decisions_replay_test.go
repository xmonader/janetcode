package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/jev"
)

const replayOKBody = `{"model":"jev-1.13.0","answers":{
  "caused_by_change":{"type":"noul","noul":0.2},
  "failure_class":{"type":"choice","choice":"environmental","confidence":0.9}},
  "usage":{"input_tokens":50}}`

// replayClient points a client at srv with the test key the client
// requires.
func replayClient(url string) *jev.Client {
	return &jev.Client{BaseURL: url, Key: "k", Model: "jev-1.13.0", Timeout: jev.MaxTimeout}
}

func replayTestQuestions() []jev.Question {
	return []jev.Question{{ID: "caused_by_change", Kind: jev.Noul, Instructions: "Caused by the change?"}}
}

// recordingSleep stands in for the backoff so the tests are instant,
// and records what the wrapper decided to wait.
func recordingSleep(waits *[]time.Duration) func(context.Context, time.Duration) error {
	return func(ctx context.Context, d time.Duration) error {
		*waits = append(*waits, d)
		return ctx.Err()
	}
}

func TestReplayRetriesRateLimitThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		_, _ = w.Write([]byte(replayOKBody))
	}))
	defer srv.Close()
	var waits []time.Duration
	res, err := evaluateRetrying(context.Background(), replayClient(srv.URL), "s",
		replayTestQuestions(), recordingSleep(&waits))
	if err != nil {
		t.Fatalf("a 429 followed by a 200 failed: %v", err)
	}
	if p, ok := res.NoulAt("caused_by_change"); !ok || p != 0.2 {
		t.Fatalf("answer = %v %v", p, ok)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
	if len(waits) != 1 || waits[0] != replayBackoffBase {
		t.Fatalf("waits = %v", waits)
	}
}

// Retry-After wins over the computed backoff: the server knows when it
// will answer again and the replay is in no hurry.
func TestReplayHonoursRetryAfter(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(429)
			return
		}
		_, _ = w.Write([]byte(replayOKBody))
	}))
	defer srv.Close()
	var waits []time.Duration
	if _, err := evaluateRetrying(context.Background(), replayClient(srv.URL), "s",
		replayTestQuestions(), recordingSleep(&waits)); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != 3*time.Second {
		t.Fatalf("waits = %v, want one 3s wait", waits)
	}
	// Still capped: a server asking for an hour does not get one.
	if d := replayBackoff(time.Hour, 1); d != replayBackoffMax {
		t.Fatalf("capped backoff = %v", d)
	}
}

// A server that is down stays down: the wrapper gives up and hands
// back the real status error, which the replay counts as an error row.
// The client's own circuit (three consecutive errors) bites before the
// attempt bound, so the server sees three calls, not four.
func TestReplayGivesUpOnPersistent500(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"message":"over capacity"}}`))
	}))
	defer srv.Close()
	var waits []time.Duration
	_, err := evaluateRetrying(context.Background(), replayClient(srv.URL), "s",
		replayTestQuestions(), recordingSleep(&waits))
	var se *jev.StatusError
	if !errors.As(err, &se) || se.StatusCode != 500 {
		t.Fatalf("err = %v, want the 500 status error", err)
	}
	if n := hits.Load(); n != 3 || n > replayMaxAttempts {
		t.Fatalf("hits = %d, want the 3 the circuit allows (bound %d)", n, replayMaxAttempts)
	}
}

// A request the server refused on its merits is not worth repeating.
func TestReplayDoesNotRetryBadRequest(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad question"}}`))
	}))
	defer srv.Close()
	var waits []time.Duration
	_, err := evaluateRetrying(context.Background(), replayClient(srv.URL), "s",
		replayTestQuestions(), recordingSleep(&waits))
	if err == nil {
		t.Fatal("a 400 answered as success")
	}
	if hits.Load() != 1 || len(waits) != 0 {
		t.Fatalf("hits = %d, waits = %v: a 400 was retried", hits.Load(), waits)
	}
}

// A cancelled run stops in the backoff instead of serving out its
// attempts.
func TestReplayStopsOnCancelledContext(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(503)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	_, err := evaluateRetrying(ctx, replayClient(srv.URL), "s", replayTestQuestions(),
		func(c context.Context, d time.Duration) error {
			cancel()
			return sleepCtx(c, d)
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1: the cancelled run kept calling", hits.Load())
	}
}
