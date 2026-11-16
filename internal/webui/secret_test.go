package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
)

func waitSecret(t *testing.T, s *server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.secretPending() != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the secret request never came up")
}

func postSecret(h http.Handler, body map[string]any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/secret", bytes.NewReader(b)))
	return rec
}

// The value goes from the POST body to the asker and nowhere else: not the
// event log every page is replayed, not /api/state, not the status.
func TestSecretValueReachesOnlyTheAsker(t *testing.T) {
	s := newServer(context.Background(), Options{Version: "test"})
	h := s.handler()
	const value = "tok-9f8e7d6c5b4a"
	out := make(chan agent.SecretReply, 1)
	go func() {
		r, err := s.AskSecret(context.Background(), agent.SecretRequest{Name: "deploy", EnvVar: "JANETCODE_SECRET_DEPLOY", Purpose: "release", Usage: "curl"})
		if err != nil {
			t.Error(err)
		}
		out <- r
	}()
	waitSecret(t, s)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if !strings.Contains(rec.Body.String(), `"env_var":"JANETCODE_SECRET_DEPLOY"`) {
		t.Fatalf("state does not show the open request: %s", rec.Body)
	}
	if rec := postSecret(h, map[string]any{"value": value}); rec.Code != 200 {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body)
	}
	r := <-out
	if r.Declined || r.Value != value {
		t.Fatalf("reply = %+v", r)
	}
	s.mu.Lock()
	logged, _ := json.Marshal(s.log)
	s.mu.Unlock()
	if strings.Contains(string(logged), value) {
		t.Fatal("the value is in the event log")
	}
	if !strings.Contains(string(logged), `"type":"secret"`) {
		t.Fatal("the request never reached the event stream")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if strings.Contains(rec.Body.String(), value) || strings.Contains(rec.Body.String(), "JANETCODE_SECRET_DEPLOY") {
		t.Fatalf("state still shows the answered request: %s", rec.Body)
	}
	if rec := postSecret(h, map[string]any{"value": value}); rec.Code != http.StatusConflict {
		t.Fatalf("a second answer = %d, want 409", rec.Code)
	}
}

func TestSecretDeclineAndEmptyValue(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"declined": {"declined": true},
		"empty":    {"value": ""},
	} {
		s := newServer(context.Background(), Options{Version: "test"})
		out := make(chan agent.SecretReply, 1)
		go func() {
			r, _ := s.AskSecret(context.Background(), agent.SecretRequest{Name: "x", EnvVar: "JANETCODE_SECRET_X"})
			out <- r
		}()
		waitSecret(t, s)
		postSecret(s.handler(), body)
		if r := <-out; !r.Declined || r.Value != "" {
			t.Errorf("%s: reply = %+v", name, r)
		}
	}
}

func TestSecretCancelDeclinesAndClears(t *testing.T) {
	s := newServer(context.Background(), Options{Version: "test"})
	out := make(chan agent.SecretReply, 1)
	go func() {
		r, _ := s.AskSecret(context.Background(), agent.SecretRequest{Name: "x", EnvVar: "JANETCODE_SECRET_X"})
		out <- r
	}()
	waitSecret(t, s)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/cancel", nil))
	if r := <-out; !r.Declined {
		t.Fatalf("reply = %+v", r)
	}
	if s.secretPending() != nil {
		t.Fatal("cancel left the request open")
	}
}

func TestSecretAskEndsWithItsContext(t *testing.T) {
	s := newServer(context.Background(), Options{Version: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := s.AskSecret(ctx, agent.SecretRequest{Name: "x"})
		errc <- err
	}()
	waitSecret(t, s)
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("no error")
	}
	if s.secretPending() != nil {
		t.Fatal("request left open after its context ended")
	}
}

// The page must use a password field, and the card must exist.
func TestSecretCardUsesAPasswordField(t *testing.T) {
	b, err := os.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	i := strings.Index(html, `id="secret-input"`)
	if i < 0 {
		t.Fatal("no secret input in index.html")
	}
	tag := html[strings.LastIndex(html[:i], "<input"):]
	tag = tag[:strings.Index(tag, ">")]
	if !strings.Contains(tag, `type="password"`) {
		t.Fatalf("secret input is not a password field: %s", tag)
	}
}
