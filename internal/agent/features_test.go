package agent

import (
	"sync"
	"testing"
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
