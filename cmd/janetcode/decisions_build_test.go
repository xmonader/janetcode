package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/jev"
)

// The composition root is the piece the unit tests cannot see: whether
// a config block actually reaches the agent, with the key resolved and
// the scrubber wired.
func TestStepDecisionsWiresTheClient(t *testing.T) {
	d, sess := buildTestDeps(t)
	d.cfg = &config.Config{Model: "m", Decisions: &config.DecisionsConfig{
		Model: "jev-1.13.0", Triage: true, TimeoutMs: 40,
	}}
	t.Setenv("JEV_TEST_KEY", "sk-test")
	d.cfg.Decisions.APIKeyEnv = "JEV_TEST_KEY"
	b := newAgentBuilder(d, sess, nil)
	if err := b.stepCore(); err != nil {
		t.Fatal(err)
	}
	if err := b.stepDecisions(); err != nil {
		t.Fatalf("stepDecisions: %v", err)
	}
	if b.ag.Decisions == nil {
		t.Fatal("a configured block with a key wired no decision provider")
	}
	if !b.ag.DecisionsTriage {
		t.Fatal("triage: true did not reach the agent")
	}
	if !b.ag.Decisions.Available() {
		t.Fatal("a fresh client must have a closed circuit")
	}
	// The timeout is the configured one, clamped only by the ceiling.
	if cl, ok := b.ag.Decisions.(*jev.Client); !ok {
		t.Fatalf("Decisions is %T, want *jev.Client", b.ag.Decisions)
	} else if cl.Timeout != 40e6 {
		t.Fatalf("timeout = %v", cl.Timeout)
	}
	if cl := b.ag.Decisions.(*jev.Client); cl.Scrub == nil {
		t.Fatal("no scrubber wired at the wire")
	}
}

// The scrubbing hook is at the wire, so a credential anywhere in a
// state — including the failing command line, which no per-field
// scrubber covered — never leaves the process, while the evidence
// around it does.
func TestStepDecisionsScrubsSecretsOffTheWire(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.5}}}`))
	}))
	defer srv.Close()
	d, sess := buildTestDeps(t)
	d.cfg = &config.Config{Model: "m", Decisions: &config.DecisionsConfig{
		Model: "jev-1.13.0", Triage: true, BaseURL: srv.URL,
	}}
	t.Setenv("JEV_TEST_KEY", "sk-test")
	d.cfg.Decisions.APIKeyEnv = "JEV_TEST_KEY"
	b := newAgentBuilder(d, sess, nil)
	if err := b.stepCore(); err != nil {
		t.Fatal(err)
	}
	if err := b.stepDecisions(); err != nil {
		t.Fatal(err)
	}
	const secret = "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"
	state := "Failing check: curl -H \"Authorization: Bearer " + secret + "\" http://api\n" +
		"Output: 401 Unauthorized while POSTing to /v1/ingest\n"
	if _, err := b.ag.Decisions.Evaluate(context.Background(), state,
		[]jev.Question{{ID: "q", Kind: jev.Noul, Instructions: "?"}}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(secret)) {
		t.Fatalf("the key reached the wire: %s", body)
	}
	// Everything that is not the key must survive: a blanked field is a
	// decision made blind.
	for _, keep := range []string{"Failing check", "401 Unauthorized", "/v1/ingest", "[redacted]"} {
		if !bytes.Contains(body, []byte(keep)) {
			t.Fatalf("scrubbing lost %q from the state: %s", keep, body)
		}
	}
}

// A block with no key is a stderr note and no feature, not a failed
// build.
func TestStepDecisionsWithoutAKey(t *testing.T) {
	d, sess := buildTestDeps(t)
	d.cfg = &config.Config{Model: "m", Decisions: &config.DecisionsConfig{Model: "jev-1.13.0"}}
	d.cfg.Decisions.APIKeyEnv = "JEV_TEST_MISSING"
	if _, defined := os.LookupEnv("JEV_TEST_MISSING"); defined {
		t.Skip("the environment really defines JEV_TEST_MISSING")
	}
	b := newAgentBuilder(d, sess, nil)
	if err := b.stepCore(); err != nil {
		t.Fatal(err)
	}
	if err := b.stepDecisions(); err != nil {
		t.Fatalf("stepDecisions: %v", err)
	}
	if b.ag.Decisions != nil {
		t.Fatal("a keyless block wired a provider anyway")
	}
	if b.ag.DecisionsTriage {
		t.Fatal("keyless wiring left the agent partially enabled")
	}
}
