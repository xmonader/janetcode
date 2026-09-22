package agent

import (
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/verify"
)

// The switch every consumer asks: off means no call is made at all, and
// the kill switch beats the configuration either way.
func TestDecisionsForIsTheOneGate(t *testing.T) {
	fd := &fakeDecisions{avail: true}
	a := &Agent{}
	a.SetDecisions(fd, true, true, true, true)
	for _, c := range []DecisionConsumer{DecisionTriage, DecisionIntake, DecisionRoute, DecisionOutcome} {
		if !a.DecisionsOn(c) || a.decisionsFor(c) == nil {
			t.Fatalf("%s should be on", c)
		}
	}
	// One consumer off leaves the others alone.
	a.SetDecisions(fd, false, true, true, true)
	if a.DecisionsOn(DecisionTriage) || a.decisionsFor(DecisionTriage) != nil {
		t.Fatal("triage was switched off")
	}
	if !a.DecisionsOn(DecisionIntake) {
		t.Fatal("intake was not switched off")
	}
	// The master switch: no client, nothing asked, every consumer flag
	// still whatever it was.
	a.SetDecisions(nil, true, true, true, true)
	for _, c := range []DecisionConsumer{DecisionTriage, DecisionIntake, DecisionOutcome} {
		if a.decisionsFor(c) != nil {
			t.Fatalf("%s asked with no client", c)
		}
	}
	if a.DecisionsClient() {
		t.Fatal("no client is configured")
	}
	// And the kill switch beats all of it.
	a.SetDecisions(fd, true, true, true, true)
	a.Off = map[string]bool{"decisions": true}
	for _, c := range []DecisionConsumer{DecisionTriage, DecisionIntake, DecisionRoute, DecisionOutcome} {
		if a.DecisionsOn(c) || a.decisionsFor(c) != nil {
			t.Fatalf("JANETCODE_OFF=decisions did not stop %s", c)
		}
	}
	if fd.calls != 0 {
		t.Fatalf("the gate made %d calls; it must make none", fd.calls)
	}
}

// The knobs a front-end changes are read on the turn goroutine, so the
// change has to be safe under -race.
func TestFeatureKnobsAreSafeToChangeWhileRead(t *testing.T) {
	a := &Agent{}
	a.SetDecisions(&fakeDecisions{avail: true}, true, false, false, false)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = a.EscalateRounds()
			_ = a.StaleNudgeOn()
			_, _ = a.advisorForcedSettings()
			_ = a.AdvisorModelName()
			_ = a.StrongModelName()
			_ = a.decisionsFor(DecisionTriage)
		}
	}()
	for i := 0; i < 200; i++ {
		a.SetEscalateAfter(i % 3)
		a.SetStaleCheckNudge(i%2 == 0)
		a.SetAdvisorMode(AdvisorAlways, i%4)
		a.SetAdvisorModel("advisor-model")
		a.SetStrongModel("strong-model")
		a.SetDecisions(&fakeDecisions{avail: true}, i%2 == 0, false, false, false)
	}
	close(stop)
	wg.Wait()
}

// Nothing configured behaves exactly as it did: every knob off, no
// consumer on, no cascade.
func TestZeroAgentHasEveryFeatureOff(t *testing.T) {
	a := &Agent{}
	if a.EscalateRounds() != 0 || a.StaleNudgeOn() || a.AdvisorModelName() != "" || a.StrongModelName() != "" {
		t.Fatal("a fresh agent has a feature on")
	}
	if a.EscalateEnabled() {
		t.Fatal("the cascade cannot fire with nothing configured")
	}
	if a.advisorForcedEnabled() {
		t.Fatal("the forced advisor is off by default")
	}
	for _, c := range []DecisionConsumer{DecisionTriage, DecisionIntake, DecisionRoute, DecisionOutcome} {
		if a.DecisionsOn(c) || a.decisionsFor(c) != nil {
			t.Fatalf("%s is on with nothing configured", c)
		}
	}
}

// The consumers, end to end: with the feature switched off mid-session,
// the one that would have made a call makes none and the turn behaves
// exactly as it did before the feature existed.
func TestSwitchingDecisionsOffMakesNoCalls(t *testing.T) {
	fd := &fakeDecisions{avail: true, res: triageResult(0.1, "environmental", 0.95)}
	a := &Agent{}
	a.SetDecisions(fd, true, false, false, false)
	res := verify.Result{Failed: "go test ./...", Exit: 1, Output: "--- FAIL: TestX\n    x_test.go:9: want 2, got 3"}
	if skip := a.triageFailure(t.Context(), res, func(Event) {}); !skip {
		t.Fatal("a confident environmental verdict skips the repair round")
	}
	if fd.calls != 1 {
		t.Fatalf("%d calls, want 1", fd.calls)
	}
	// /jev off: no client, so nothing is asked and the failure is sent
	// back as it always was.
	a.SetDecisions(nil, true, false, false, false)
	if skip := a.triageFailure(t.Context(), res, func(Event) {}); skip {
		t.Fatal("with the feature off the repair round must be spent")
	}
	if fd.calls != 1 {
		t.Fatalf("%d calls after /jev off, want the same 1", fd.calls)
	}
	// And the kill switch, with the client back in place.
	a.SetDecisions(fd, true, false, false, false)
	a.Off = map[string]bool{"decisions": true}
	if skip := a.triageFailure(t.Context(), res, func(Event) {}); skip {
		t.Fatal("JANETCODE_OFF=decisions must leave the repair round alone")
	}
	if fd.calls != 1 {
		t.Fatalf("%d calls under the kill switch, want the same 1", fd.calls)
	}
}
