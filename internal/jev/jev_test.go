package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
  "usage": {"input_tokens": 100, "output_tokens": 10, "cost": 0.00001806}
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
	// What the call was billed, as the server reported it: the harness
	// has no rate table for a decisions model, so an unreported figure
	// must stay zero rather than become an estimate.
	if res.TokensOut != 10 || res.Cost != 0.00001806 {
		t.Fatalf("usage = %d out, $%v", res.TokensOut, res.Cost)
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

// A noul's criteria is optional: present verbatim when set, and the
// key absent entirely when not, so existing payloads are unchanged.
func TestNoulCriteriaMarshalling(t *testing.T) {
	plain, err := json.Marshal(Question{ID: "caused", Kind: Noul, Instructions: "Caused by the change?"})
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != `{"instructions":"Caused by the change?","type":"noul"}` {
		t.Fatalf("noul without criteria = %s", plain)
	}
	withC, err := json.Marshal(Question{ID: "caused", Kind: Noul, Instructions: "Caused by the change?",
		NoulCriteria: map[string]string{"true": "the change broke it", "false": "the environment did"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"criteria":{"false":"the environment did","true":"the change broke it"},` +
		`"instructions":"Caused by the change?","type":"noul"}`
	if string(withC) != want {
		t.Fatalf("noul with criteria = %s", withC)
	}
}

func TestEvaluateSoftErrors(t *testing.T) {
	srv, _ := fakeServer(t, 500, `{"error":{"message":"over capacity"}}`)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil || !strings.Contains(err.Error(), "over capacity") {
		t.Fatalf("err = %v", err)
	}
}

// A non-200 carries its status and Retry-After to the caller so the
// offline replay can tell a rate limit from a bad request. The message
// is unchanged, and so is the soft-error contract.
func TestStatusErrorCarriesStatusAndRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	_, err := c.Evaluate(context.Background(), "s", testQuestions())
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *StatusError", err, err)
	}
	if se.StatusCode != 429 || se.Message != "slow down" || se.RetryAfter != 2*time.Second {
		t.Fatalf("status error = %+v", se)
	}
	if !se.Retryable() {
		t.Fatal("429 is not retryable")
	}
	if se.Error() != "decisions 429: slow down" {
		t.Fatalf("message = %q", se.Error())
	}
	if (&StatusError{StatusCode: 400}).Retryable() || !(&StatusError{StatusCode: 503}).Retryable() {
		t.Fatal("retryable classification is wrong")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"3":                             3 * time.Second,
		"0":                             0,
		"-5":                            0,
		"nonsense":                      0,
		"Mon, 21 Sep 2026 12:00:30 GMT": 30 * time.Second,
		"Mon, 21 Sep 2026 11:59:00 GMT": 0, // already past
	}
	for h, want := range cases {
		if got := parseRetryAfter(h, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", h, got, want)
		}
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

// After the cooldown an open circuit lets exactly one probe through:
// a failing probe buys another cooldown, a succeeding one closes the
// circuit for good and the session gets its triage back.
func TestCircuitHalfOpensAfterTheCooldown(t *testing.T) {
	var hits atomic.Int32
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(okBody))
	}))
	defer srv.Close()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0",
		Cooldown: 5 * time.Minute, now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil {
			t.Fatal("expected error")
		}
	}
	if c.Available() || hits.Load() != 3 {
		t.Fatalf("circuit not open: available=%v hits=%d", c.Available(), hits.Load())
	}
	// Inside the cooldown nothing leaves the process.
	now = now.Add(4 * time.Minute)
	if c.Available() {
		t.Fatal("available before the cooldown elapsed")
	}
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil {
		t.Fatal("expected the open circuit to refuse")
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3 (no call inside the cooldown)", hits.Load())
	}
	// Cooldown elapsed: one probe, which fails and buys another cooldown.
	now = now.Add(2 * time.Minute)
	if !c.Available() {
		t.Fatal("no probe due after the cooldown")
	}
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil {
		t.Fatal("expected the probe to fail")
	}
	if hits.Load() != 4 {
		t.Fatalf("hits = %d, want 4 (exactly one probe)", hits.Load())
	}
	if c.Available() {
		t.Fatal("a failed probe left the circuit available")
	}
	// Next cooldown, service healthy: the probe closes the circuit.
	healthy.Store(true)
	now = now.Add(5 * time.Minute)
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err != nil {
		t.Fatalf("probe against a healthy service: %v", err)
	}
	if !c.Available() {
		t.Fatal("a successful probe did not close the circuit")
	}
	// And the circuit stays closed: the streak that opened it is spent.
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err != nil {
		t.Fatalf("call after recovery: %v", err)
	}
	if hits.Load() != 6 {
		t.Fatalf("hits = %d, want 6", hits.Load())
	}
}

// Only one probe is ever in flight, however many turns race for it.
func TestOnlyOneProbeInFlight(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) > 3 { // the probe: hold it open while the others race
			<-release
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0", Timeout: MaxTimeout,
		Cooldown: time.Minute, now: func() time.Time { return now }}
	for i := 0; i < 3; i++ {
		_, _ = c.Evaluate(context.Background(), "s", testQuestions())
	}
	now = now.Add(2 * time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Evaluate(context.Background(), "s", testQuestions())
		}()
	}
	// The probe holder blocks until its own timeout; the other seven are
	// refused locally, so the server must see exactly one more request.
	wg.Wait()
	close(release)
	if hits.Load() != 4 {
		t.Fatalf("hits = %d, want 4 (three to open, one probe)", hits.Load())
	}
}

func TestNoBreakerKeepsCallingAfterABurstOfErrors(t *testing.T) {
	srv, hits := fakeServer(t, 503, `{}`)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0", NoBreaker: true}
	for i := 0; i < 6; i++ {
		if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err == nil {
			t.Fatal("expected error")
		}
	}
	if !c.Available() {
		t.Fatal("NoBreaker client opened its circuit")
	}
	if hits.Load() != 6 {
		t.Fatalf("hits = %d, want 6 (every call must reach the server)", hits.Load())
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

// A call the caller abandoned — the user interrupted the turn — says
// nothing about the service. Counting it opened the circuit after three
// interrupted gate failures, and triage paused itself for the cooldown
// with a notice blaming "repeated errors" on a service that never
// failed once.
func TestACallerCancellationDoesNotTripTheBreaker(t *testing.T) {
	srv, hits := fakeServer(t, 200, okBody)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < maxConsecErrors+1; i++ {
		if _, err := c.Evaluate(ctx, "s", testQuestions()); err == nil {
			t.Fatal("a cancelled call answered")
		}
	}
	if !c.Available() {
		t.Fatal("caller cancellations opened the circuit")
	}
	if _, err := c.Evaluate(context.Background(), "s", testQuestions()); err != nil || hits.Load() != 1 {
		t.Fatalf("after the cancellations: err %v, hits %d", err, hits.Load())
	}

	// A half-open probe the caller abandons settles nothing either way:
	// the next call may probe at once rather than wait a fresh cooldown.
	now := time.Unix(0, 0)
	d := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0", now: func() time.Time { return now }}
	d.open, d.openedAt = true, now.Add(-2*DefaultCooldown)
	if _, err := d.Evaluate(ctx, "s", testQuestions()); err == nil {
		t.Fatal("a cancelled probe answered")
	}
	if !d.Available() {
		t.Fatal("an abandoned probe re-opened the circuit for another cooldown")
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
		"jev-..":               false,
		"jev-1..2":             false,
		"jev-1.2.":             false,
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
		c.record(1100*time.Millisecond, nil, false)
	}
	if !c.Available() {
		t.Fatal("the window is not full yet; it must not open early")
	}
	c.record(1200*time.Millisecond, nil, false)
	if c.Available() {
		t.Fatal("ten successful calls at >1s median left the circuit closed")
	}
	// Nine fast calls and one slow one stay open: the median a full
	// window of mostly-fast calls produces is fast.
	c2 := &Client{}
	for i := 0; i < LatencyWindow-1; i++ {
		c2.record(3*time.Millisecond, nil, false)
	}
	c2.record(1200*time.Millisecond, nil, false)
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
