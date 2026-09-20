package main

import (
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
	// The scrubber redacts credential-shaped state fields.
	if b.ag.ScrubState == nil {
		t.Fatal("no scrubber wired")
	}
	if got := b.ag.ScrubState("api_key = sk-ant-abcdefabcdefabcdef1234"); got != "[redacted]" {
		t.Fatalf("ScrubState = %q", got)
	}
	if got := b.ag.ScrubState("ordinary note"); got != "ordinary note" {
		t.Fatalf("ScrubState mangled plain text: %q", got)
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
	if b.ag.ScrubState != nil || b.ag.DecisionsTriage {
		t.Fatal("keyless wiring left the agent partially enabled")
	}
}
