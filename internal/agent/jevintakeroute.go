package agent

import (
	"context"
	"time"

	"github.com/xmonader/janetcode/internal/jev"
)

// Routing on the intake classification (jevintake.go), behind
// `decisions.intake_route` and off by default.
//
// This is the first consumer that acts on an intake answer, and it acts
// on exactly one knob the harness already has: `route_cheap`, the model
// the step router (steproute.go) sends mechanical steps to. No tier
// system is invented here. All this consumer does is let that existing
// route start at the first step of the session's first turn — which the
// step router otherwise refuses, because it has nothing to judge a turn
// by until a step has been taken.
//
// The costly error is routing hard work to the weak model, so the gate
// is deliberately lopsided: only a turn the classifier is confident is
// a small, read-only request — an explain, low complexity, no plan
// wanted — is routed, and any uncertainty anywhere leaves the turn
// on the session's own model exactly as with the feature off. A wrong
// "route" costs a weak answer to a question; a wrong "do not route"
// costs nothing at all. Everything else about the step router still
// applies on top: a tainted turn, a warned turn or a non-mechanical
// step stops the route at once.

// The routing thresholds. They come from the replay table in
// docs/notes/jev.md ("Intake routing", 2026-09-21), not from taste: at
// these values, over five scoring runs of the 41-row corpus, no cheap
// route was taken on a row labelled strong.
const (
	// routeKindConfidence is how sure the classifier must be of the kind
	// before the kind gate below means anything.
	routeKindConfidence = 0.8
	// routeComplexityMax is the highest complexity that still routes, on
	// the intake rubric's 0-4 index: 0 is "one look", 1 "a small edit".
	routeComplexityMax = 1.0
	// routeComplexityConf is the confidence that complexity score needs.
	// An unsure complexity is an uncertain turn, and an uncertain turn is
	// never downgraded.
	routeComplexityConf = 0.6
	// routeNeedsPlanMax is the highest p(needs a plan) that still routes.
	routeNeedsPlanMax = 0.5
)

// routeKinds are the request kinds a cheap route is allowed for. docs
// is excluded although it is often small: it writes. review is excluded
// on measurement rather than principle — it asks for no change either,
// but it is the kind the classifier cannot size. "tell me whether the
// private-address guard can be bypassed" names one file and scores 0.23
// on the rubric while meaning "read the guard and everything that calls
// it", and it was the only costly wrong route in five replay runs.
// Adding review back buys two more cheap routes out of twelve and costs
// that one; the table is in docs/notes/jev.md (2026-09-21).
var routeKinds = map[string]bool{"explain": true}

// IntakeRoute is what the routing consumer decided about a turn, and
// why. Why is filled in both directions so the session log says what
// stopped a route, not only that none was taken.
type IntakeRoute struct {
	Cheap bool
	Why   string
}

// IntakeRouting maps an intake classification onto that decision. It is
// a pure function of the answers — no clock, no config, no agent — so
// the offline replay scores the very mapping a live turn takes.
// Exported for the replay driver for that reason.
func IntakeRouting(res *jev.Result) IntakeRoute {
	if res == nil {
		return IntakeRoute{Why: "no answer"}
	}
	kind, kindConf, ok := res.ChoiceAt(qIntakeKind)
	if !ok {
		return IntakeRoute{Why: "no kind"}
	}
	if !routeKinds[kind] {
		return IntakeRoute{Why: "kind " + kind + " changes the repository"}
	}
	if kindConf < routeKindConfidence {
		return IntakeRoute{Why: "kind " + kind + " not confident enough"}
	}
	complexity, _, complexityConf, ok := res.ScoreAt(qIntakeComplexity)
	if !ok {
		return IntakeRoute{Why: "no complexity"}
	}
	if complexityConf < routeComplexityConf {
		return IntakeRoute{Why: "complexity uncertain"}
	}
	if complexity > routeComplexityMax {
		return IntakeRoute{Why: "complexity too high"}
	}
	if plan, ok := res.NoulAt(qIntakeNeedsPlan); ok && plan > routeNeedsPlanMax {
		return IntakeRoute{Why: "wants a plan"}
	}
	return IntakeRoute{Cheap: true, Why: "small read-only " + kind}
}

// intakeRouteDeadline bounds how long the first turn waits for the
// classification when routing is on. Routing has to be decided before
// the first model request, so unlike the log-only consumer this one does
// cost the turn something — this much, once a session, and only when the
// answer has not already arrived during the turn's own preparation. Past
// it the turn runs on the session's model exactly as with the feature
// off.
const intakeRouteDeadline = 700 * time.Millisecond

// awaitIntakeRouting collects the classification in time to route on it.
// It is called once, at the end of the turn's preparation, and only when
// the routing consumer is on: with routing off the classification stays
// what it was, an answer nobody waits for.
//
// Whatever it collects is recorded here, which is also what stops
// drainIntake recording it a second time at the end of the turn.
func (a *Agent) awaitIntakeRouting(ctx context.Context) {
	if !a.DecisionsIntakeRoute || a.intakeCh == nil {
		return
	}
	wait := intakeRouteDeadline
	if left := time.Until(a.intakeDeadline); left < wait {
		wait = left
	}
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case out := <-a.intakeCh:
		a.intakeCh, a.intakeDone = nil, true
		if out.err == nil && out.res != nil {
			// Pinned to the turn it classified. A later turn of the same
			// session ("now implement it") is a different request that
			// nothing has classified, and must not inherit this route.
			a.intakeRoute, a.intakeRouteTurn = IntakeRouting(out.res), a.turnN
		}
		a.recordIntake(out)
	case <-t.C:
		// Left in flight on purpose: drainIntake still collects and logs
		// it at the end of the turn, so the call is paid for and recorded
		// either way. It just does not get to route.
	case <-ctx.Done():
	}
}

// intakeRoutedCheap reports whether the session's first prompt was
// classified as small enough to start on route_cheap. It is read by the
// step router (steproute.go) and is false whenever the consumer is off,
// the answer was late, or the classification did not clear the gate.
//
// It is also false past the turn the classification was made for.
func (a *Agent) intakeRoutedCheap() bool {
	return a.DecisionsIntakeRoute && a.intakeRoute.Cheap && a.turnN == a.intakeRouteTurn
}
