package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/session"
)

func outcomeResult(outcome string, conf float64) *jev.Result {
	return &jev.Result{Model: "jev-1.13.0", TokensIn: 300, TokensOut: 20, Cost: 0.0000221,
		Answers: map[string]jev.Answer{
			qSessionOutcome: {Kind: jev.Choice, Choice: outcome, Confidence: conf}}}
}

func outcomeEvents(a *Agent) []session.Event {
	var out []session.Event
	for _, e := range a.Log.Events() {
		if e.Type == session.TypeDecision && e.Name == "session_outcome" {
			out = append(out, e)
		}
	}
	return out
}

func TestSessionOutcomeIsClassifiedLoggedAndCounted(t *testing.T) {
	fd := &fakeDecisions{res: outcomeResult("partial", 0.74), avail: true}
	a := &Agent{Log: &session.Memory{}, Decisions: fd, DecisionsSessionOutcome: true}
	for _, e := range annEvents() {
		a.Log.Append(e)
	}
	a.ClassifySessionOutcome(context.Background())
	if fd.calls != 1 {
		t.Fatalf("calls = %d, want 1", fd.calls)
	}
	evs := outcomeEvents(a)
	if len(evs) != 1 {
		t.Fatalf("events = %d, want 1", len(evs))
	}
	extra := string(evs[0].Extra)
	for _, want := range []string{`"consumer":"session_outcome"`, `"outcome":"partial"`,
		`"outcome_confidence":0.74`, `"acted_on":false`, `"turns":2`} {
		if !strings.Contains(extra, want) {
			t.Fatalf("Extra missing %s: %s", want, extra)
		}
	}
	if st := a.Stats(); st.DecisionCalls != 1 || st.DecisionActed != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

// Every way the consumer can be absent leaves the session with no call
// made and nothing written — the flag-off case included.
func TestSessionOutcomeOffMakesNoCall(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*Agent, *fakeDecisions)
	}{
		{"flag off", func(a *Agent, _ *fakeDecisions) { a.DecisionsSessionOutcome = false }},
		{"decisions switched off", func(a *Agent, _ *fakeDecisions) { a.Off = map[string]bool{"decisions": true} }},
		{"no client", func(a *Agent, _ *fakeDecisions) { a.Decisions = nil }},
		{"circuit open", func(_ *Agent, fd *fakeDecisions) { fd.avail = false }},
	} {
		t.Run(c.name, func(t *testing.T) {
			fd := &fakeDecisions{res: outcomeResult("success", 0.9), avail: true}
			a := &Agent{Log: &session.Memory{}, Decisions: fd, DecisionsSessionOutcome: true}
			for _, e := range annEvents() {
				a.Log.Append(e)
			}
			c.setup(a, fd)
			a.ClassifySessionOutcome(context.Background())
			if fd.calls != 0 || len(outcomeEvents(a)) != 0 {
				t.Fatalf("calls = %d, events = %d, want none", fd.calls, len(outcomeEvents(a)))
			}
		})
	}
	// A session with no turns has nothing to classify and is not charged
	// for asking.
	fd := &fakeDecisions{res: outcomeResult("success", 0.9), avail: true}
	a := &Agent{Log: &session.Memory{}, Decisions: fd, DecisionsSessionOutcome: true}
	a.ClassifySessionOutcome(context.Background())
	if fd.calls != 0 {
		t.Fatalf("an empty session should not be classified")
	}
}

func TestSessionOutcomeStateIsOneLinePerTurn(t *testing.T) {
	state := SessionOutcomeState(AnnotateTurns(annEvents()))
	for _, want := range []string{"turn 1: make the parser accept trailing commas | 2 tool calls, 1 failed",
		"turn 2: now fix the test | 1 tool calls, 0 failed"} {
		if !strings.Contains(state, want) {
			t.Fatalf("state missing %q:\n%s", want, state)
		}
	}
}
