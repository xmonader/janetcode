package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer answers with body and records how many requests it saw.
func fakeServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("no bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

const okBody = `{
  "model": "jev-1.13.0",
  "answers": {
    "caused": {"type": "noul", "noul": 0.9},
    "class": {"type": "choice", "choice": "patch", "confidence": 0.8, "probabilities": {"patch": 0.8, "flaky": 0.2}},
    "severity": {"type": "score", "score": 1.035, "confidence": 0.7, "legend": {"0": "low", "1": "mid", "2": "high"}, "probabilities": {"0": 0.1, "1": 0.755, "2": 0.145}}
  },
  "usage": {"input_tokens": 100, "output_tokens": 10}
}`

func testQuestions() []Question {
	return []Question{
		{ID: "caused", Kind: Noul, Instructions: "Is this caused by the change?"},
		{ID: "class", Kind: Choice, Instructions: "What kind of failure?", Options: map[string]string{"patch": "the change", "flaky": "flaky"}},
		{ID: "severity", Kind: Score, Instructions: "How bad?", Levels: []string{"low", "mid", "high"}},
	}
}

func TestEvaluateAllThreeKinds(t *testing.T) {
	srv, hits := fakeServer(t, 200, okBody)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	res, err := c.Evaluate(context.Background(), "state", testQuestions())
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "jev-1.13.0" || res.TokensIn != 100 {
		t.Fatalf("meta = %+v", res)
	}
	if p, ok := res.NoulAt("caused"); !ok || p != 0.9 {
		t.Fatalf("noul = %v %v", p, ok)
	}
	if ch, conf, ok := res.ChoiceAt("class"); !ok || ch != "patch" || conf != 0.8 {
		t.Fatalf("choice = %q %v %v", ch, conf, ok)
	}
	// The API's score is a continuous expectation over the rubric while
	// the probabilities are keyed by integer level index: reading it as
	// a choice would hand callers "1.035", a key of nothing.
	sc, probs, conf, ok := res.ScoreAt("severity")
	if !ok || sc != 1.035 || conf != 0.7 || probs["1"] != 0.755 || len(probs) != 3 {
		t.Fatalf("score = %v %v %v %v", sc, probs, conf, ok)
	}
	if _, ok := res.NoulAt("class"); ok {
		t.Fatal("a choice answered as a noul")
	}
	if ch, _, ok := res.ChoiceAt("severity"); ok {
		t.Fatalf("a score answered as a choice: %q", ch)
	}
	if _, _, _, ok := res.ScoreAt("class"); ok {
		t.Fatal("a choice answered as a score")
	}
	if _, _, _, ok := res.ScoreAt("absent"); ok {
		t.Fatal("a missing answer answered as a score")
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestEvaluateSendsTheWireShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	if _, err := c.Evaluate(context.Background(), "the state", testQuestions()); err != nil {
		t.Fatal(err)
	}
	if got["state"] != "the state" || got["model"] != "jev-1.13.0" {
		t.Fatalf("request = %v", got)
	}
	qs := got["questions"].(map[string]any)
	if qs["caused"].(map[string]any)["type"] != "noul" {
		t.Fatalf("noul question = %v", qs["caused"])
	}
	// A choice's criteria is an object, a score's an array; confusing
	// them is the one wire bug the shapes invite.
	if _, isMap := qs["class"].(map[string]any)["criteria"].(map[string]any); !isMap {
		t.Fatalf("choice criteria = %v", qs["class"])
	}
	if _, isArr := qs["severity"].(map[string]any)["criteria"].([]any); !isArr {
		t.Fatalf("score criteria = %v", qs["severity"])
	}
}

func TestEvaluateSoftErrors(t *testing.T) {
	srv, _ := fakeServer(t, 500, `{"error":{"message":"over capacity"}}`)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil || !strings.Contains(err.Error(), "over capacity") {
		t.Fatalf("err = %v", err)
	}
}

func TestCircuitOpensAfterThreeErrors(t *testing.T) {
	srv, hits := fakeServer(t, 503, `{}`)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	for i := 0; i < 3; i++ {
		if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil {
			t.Fatal("expected error")
		}
	}
	if c.Available() {
		t.Fatal("circuit still closed after 3 errors")
	}
	// The fourth call is refused locally: the server sees nothing more.
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil {
		t.Fatal("expected error with open circuit")
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3 (the open circuit must not call out)", hits.Load())
	}
	calls, errs, _ := c.Stats()
	if calls != 3 || errs != 3 {
		t.Fatalf("stats = %d calls, %d errs", calls, errs)
	}
}

func TestSuccessResetsTheErrorStreak(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%3 == 0 { // every third request fails
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	for i := 0; i < 8; i++ {
		_, _ = c.Evaluate(context.Background(), "s", testQuestions())
	}
	if !c.Available() {
		t.Fatal("interleaved failures opened the circuit")
	}
}

func TestTimeoutClampedAndEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0", Timeout: 50 * time.Millisecond}
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil {
		t.Fatal("a 300ms server answered a 50ms call")
	}
	// A configured timeout above the ceiling is clamped, not honoured.
	c2 := &Client{Timeout: time.Hour}
	if c2.timeout() != MaxTimeout {
		t.Fatalf("timeout() = %v, want the %v ceiling", c2.timeout(), MaxTimeout)
	}
	c3 := &Client{}
	if c3.timeout() != DefaultTimeout {
		t.Fatalf("zero timeout = %v, want the %v default", c3.timeout(), DefaultTimeout)
	}
}

func TestScrubRewritesTheState(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0",
		Scrub: func(s string) string {
			if strings.Contains(s, "sk-ant-") {
				return "[redacted]"
			}
			return s
		}}
	if _, err := c.Evaluate(context.Background(), "key sk-ant-abc123 here", testQuestions()); err != nil {
		t.Fatal(err)
	}
	if got["state"] != "[redacted]" {
		t.Fatalf("state left unscrubbed: %v", got["state"])
	}
}

func TestPinnedModel(t *testing.T) {
	cases := map[string]bool{
		"jev-1.13.0":           true,
		"jev-2.0.1":            true,
		"~typesafe/jev-latest": true,
		"~typesafe/jev-1.13":   true,
		"jev-latest":           false,
		"jev-preview":          false,
		"~typesafe/claude":     false,
		"jev-1.13":             false,
		"":                     false,
		"claude-opus-5":        false,
	}
	for model, want := range cases {
		if got := PinnedModel(model); got != want {
			t.Errorf("PinnedModel(%q) = %v, want %v", model, got, want)
		}
	}
}

// The endpoint is per host: OpenRouter's decisions API, TypeSafe's
// native one. Wrong paths 404 in the wild, so this is pinned on both
// sides.
// The latency breaker is the other half of rung 4: ten successful
// calls with a median past a second are the service, not noise. Driven
// through record() directly, which is exactly what Evaluate feeds it.
func TestLatencyBreakerOpensOnSlowMedians(t *testing.T) {
	c := &Client{}
	for i := 0; i < LatencyWindow-1; i++ {
		c.record(1100*time.Millisecond, nil)
	}
	if !c.Available() {
		t.Fatal("the window is not full yet; it must not open early")
	}
	c.record(1200*time.Millisecond, nil)
	if c.Available() {
		t.Fatal("ten successful calls at >1s median left the circuit closed")
	}
	// Nine fast calls and one slow one stay open: the median a full
	// window of mostly-fast calls produces is fast.
	c2 := &Client{}
	for i := 0; i < LatencyWindow-1; i++ {
		c2.record(3*time.Millisecond, nil)
	}
	c2.record(1200*time.Millisecond, nil)
	if !c2.Available() {
		t.Fatal("nine fast calls and one slow one opened the circuit")
	}
	calls, errs, total := c2.Stats()
	if calls != LatencyWindow || errs != 0 || total != 9*3*time.Millisecond+1200*time.Millisecond {
		t.Fatalf("stats = %d calls, %d errs, %v", calls, errs, total)
	}
}

func TestEndpointPerHost(t *testing.T) {
	cases := map[string]string{
		"":                             "https://openrouter.ai/api/alpha/decisions",
		"https://openrouter.ai":        "https://openrouter.ai/api/alpha/decisions",
		"https://openrouter.ai/api/v1": "https://openrouter.ai/api/alpha/decisions",
		"https://api.typesafe.ai":      "https://api.typesafe.ai/v1/systemone",
		"http://127.0.0.1:9000":        "http://127.0.0.1:9000/v1/systemone",
	}
	for base, want := range cases {
		if got := (&Client{BaseURL: base}).endpoint(); got != want {
			t.Errorf("endpoint(%q) = %q, want %q", base, got, want)
		}
	}
}
