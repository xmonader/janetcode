package httpstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// testWire posts to a test server; its body is "data: <text>" frames
// ending with "data: END".
type testWire struct {
	url     string
	mu      sync.Mutex
	keys    []string
	dropped bool // learned to leave out the "extra" parameter
}

func (w *testWire) Send(ctx context.Context, req llm.Request, key string) (*http.Response, error) {
	w.mu.Lock()
	w.keys = append(w.keys, key)
	extra := !w.dropped
	w.mu.Unlock()
	return Post(ctx, http.DefaultClient, fmt.Sprintf("%s?extra=%v", w.url, extra), []byte(`{}`), map[string]string{"x-key": key})
}

func (w *testWire) Read(body io.Reader, wd *llm.Watchdog, emit func(llm.Event)) (*llm.Response, error) {
	var sb strings.Builder
	err := ReadSSE(body, wd, "test", func(_ string, data []byte) (bool, error) {
		if string(data) == "END" {
			return true, nil
		}
		emit(llm.Event{Type: llm.EventTextDelta, Text: string(data)})
		sb.Write(data)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, sb.String()), StopReason: llm.StopEndTurn}, nil
}

func (w *testWire) Learn(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) && ae.Status == 400 && strings.Contains(ae.Message, "extra") {
		w.mu.Lock()
		defer w.mu.Unlock()
		if !w.dropped {
			w.dropped = true
			return true
		}
	}
	return false
}

// script serves one canned answer per request, in order.
func script(t *testing.T, answers ...func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *int) {
	t.Helper()
	n := 0
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		if i >= len(answers) {
			t.Errorf("unexpected request %d", i+1)
			w.WriteHeader(500)
			return
		}
		answers[i](w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func status(code int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(code)
		io.WriteString(w, body)
	}
}

func sse(frames ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range frames {
			fmt.Fprintf(w, "data: %s\n\n", f)
		}
	}
}

func client(url string, keys ...string) (*Client, *testWire) {
	w := &testWire{url: url}
	return New(w, llm.NewKeyRing(keys), 2, llm.StreamLimits{}), w
}

func TestRetriesAServerErrorThenAnswers(t *testing.T) {
	srv, n := script(t, status(503, `{"error":{"message":"overloaded"}}`), sse("hi", "END"))
	c, _ := client(srv.URL, "k")
	var events []llm.EventType
	resp, err := c.Stream(context.Background(), llm.Request{Model: "m"}, func(e llm.Event) { events = append(events, e.Type) })
	if err != nil || resp.Message.Text() != "hi" || *n != 2 {
		t.Fatalf("resp=%v err=%v requests=%d", resp, err, *n)
	}
	if !contains(events, llm.EventRetried) {
		t.Fatalf("events %v lack a retried event", events)
	}
}

func TestRotatesARejectedKey(t *testing.T) {
	srv, _ := script(t, status(401, `{"error":{"message":"bad key"}}`), sse("ok", "END"))
	c, w := client(srv.URL, "k1", "k2")
	if _, err := c.Stream(context.Background(), llm.Request{Model: "m"}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(w.keys, ",") != "k1,k2" {
		t.Fatalf("keys tried: %v", w.keys)
	}
}

func TestLearnedParameterIsResentWithoutARetry(t *testing.T) {
	srv, _ := script(t,
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("extra") != "true" {
				t.Error("first request without extra")
			}
			status(400, `{"error":{"message":"unknown field extra"}}`)(w, r)
		},
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("extra") != "false" {
				t.Error("resend still carries extra")
			}
			sse("ok", "END")(w, r)
		})
	c, _ := client(srv.URL, "k")
	c.MaxRetries = 0
	if _, err := c.Stream(context.Background(), llm.Request{Model: "m"}, nil); err != nil {
		t.Fatal(err)
	}
}

// A body that ends without the terminal frame never answered: its deltas
// are discarded and the request is sent again.
func TestTruncatedStreamIsDiscardedAndRetried(t *testing.T) {
	srv, _ := script(t, sse("half"), sse("whole", "END"))
	c, _ := client(srv.URL, "k")
	var events []llm.EventType
	resp, err := c.Stream(context.Background(), llm.Request{Model: "m"}, func(e llm.Event) { events = append(events, e.Type) })
	if err != nil || resp.Message.Text() != "whole" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if !contains(events, llm.EventStreamDiscard) {
		t.Fatalf("events %v lack a discard", events)
	}
}

func TestBadRequestIsFinal(t *testing.T) {
	srv, n := script(t, status(400, `[{"error":{"code":400,"message":"Invalid value at contents","status":"INVALID_ARGUMENT"}}]`))
	c, _ := client(srv.URL, "k")
	_, err := c.Stream(context.Background(), llm.Request{Model: "m"}, nil)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "Invalid value at contents" || *n != 1 {
		t.Fatalf("err=%v requests=%d", err, *n)
	}
}

func TestReadSSENamesANonSSEBody(t *testing.T) {
	err := ReadSSE(strings.NewReader("<html>proxy login</html>\n"), nil, "x", func(string, []byte) (bool, error) { return false, nil })
	if !llm.Truncated(err) || !strings.Contains(err.Error(), "proxy login") {
		t.Fatalf("err = %v", err)
	}
	err = ReadSSE(strings.NewReader("event: a\ndata: {}\n\n"), nil, "x", func(ev string, _ []byte) (bool, error) {
		if ev != "a" {
			t.Errorf("event = %q", ev)
		}
		return false, nil
	})
	if !errors.Is(err, ErrEndedEarly) || !llm.Truncated(err) {
		t.Fatalf("err = %v", err)
	}
}

func contains(xs []llm.EventType, x llm.EventType) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
