package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// routeResult builds an intake answer with every field the routing
// mapping reads under the test's control — intakeResult pins the
// complexity confidence, and half these cases are about that number.
func routeResult(kind string, kindConf, complexity, complexityConf, needsPlan float64) *jev.Result {
	return &jev.Result{Model: "jev-1.13.0", TokensIn: 120, TokensOut: 40, Cost: 0.0000091,
		Answers: map[string]jev.Answer{
			qIntakeKind:       {Kind: jev.Choice, Choice: kind, Confidence: kindConf},
			qIntakeComplexity: {Kind: jev.Score, Score: complexity, Confidence: complexityConf},
			qIntakeNeedsPlan:  {Kind: jev.Noul, Noul: needsPlan},
		}}
}

// The mapping, exhaustively: one case per gate, each at the threshold
// and each on the side that changes the answer. The costly error this
// table exists for is a "cheap" on a row that is not small and
// read-only, so every kind that writes is here by name.
func TestIntakeRoutingMapping(t *testing.T) {
	cases := []struct {
		name      string
		res       *jev.Result
		wantCheap bool
		wantWhy   string
	}{
		{"a confident one-look explain routes", routeResult("explain", 0.99, 0.3, 0.9, 0.2), true, "small read-only explain"},
		{"review never routes: the classifier cannot size it", routeResult("review", 0.9, 0.6, 0.7, 0.4), false, "kind review changes the repository"},
		{"complexity exactly at the ceiling routes", routeResult("explain", 0.9, 1.0, 0.6, 0.49), true, "small read-only explain"},
		{"kind confidence exactly at the floor routes", routeResult("explain", 0.8, 0.5, 0.6, 0.1), true, "small read-only explain"},

		{"implement never routes", routeResult("implement", 1.0, 0.1, 1.0, 0.0), false, "kind implement changes the repository"},
		{"debug never routes", routeResult("debug", 1.0, 0.1, 1.0, 0.0), false, "kind debug changes the repository"},
		{"docs never routes, small as it looks", routeResult("docs", 1.0, 0.2, 1.0, 0.0), false, "kind docs changes the repository"},
		{"other is ambiguous, so it never routes", routeResult("other", 1.0, 0.1, 1.0, 0.0), false, "kind other changes the repository"},

		{"an unsure explain does not route", routeResult("explain", 0.79, 0.2, 0.9, 0.1), false, "kind explain not confident enough"},
		{"an unsure complexity does not route", routeResult("explain", 0.99, 0.2, 0.59, 0.1), false, "complexity uncertain"},
		{"too much work does not route", routeResult("explain", 0.99, 1.01, 0.9, 0.1), false, "complexity too high"},
		{"wanting a plan does not route", routeResult("explain", 0.99, 0.4, 0.9, 0.51), false, "wants a plan"},

		{"no answer does not route", nil, false, "no answer"},
		{"no kind does not route", &jev.Result{Answers: map[string]jev.Answer{}}, false, "no kind"},
		{"no complexity does not route", &jev.Result{Answers: map[string]jev.Answer{
			qIntakeKind: {Kind: jev.Choice, Choice: "explain", Confidence: 0.99}}}, false, "no complexity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IntakeRouting(c.res)
			if got.Cheap != c.wantCheap || got.Why != c.wantWhy {
				t.Fatalf("IntakeRouting = %+v, want cheap=%v why=%q", got, c.wantCheap, c.wantWhy)
			}
		})
	}
}

// routableRoutedAgent is routableAgent (steproute_test.go) at the first
// call of the first turn, with an intake route already decided.
func routableRoutedAgent(cheap bool) *Agent {
	a := routableAgent(true)
	a.stepN, a.stepRoute.prevMechanical = 1, false
	a.DecisionsIntakeRoute = true
	a.intakeRoute = IntakeRoute{Cheap: cheap, Why: "test"}
	return a
}

func TestRouteStepFirstCallRoutesOnlyOnAnIntakeRoute(t *testing.T) {
	a := routableRoutedAgent(true)
	prov, model, ok := a.routeStep(func(Event) {})
	if !ok || model != "cheap-model" || prov == nil {
		t.Fatalf("an intake-routed first call should route: ok=%v model=%q", ok, model)
	}
	// The consumer's own flag is the whole difference: with it off the
	// first call is refused exactly as it was before the feature.
	off := routableRoutedAgent(true)
	off.DecisionsIntakeRoute = false
	if _, _, ok := off.routeStep(func(Event) {}); ok {
		t.Fatal("intake routing off must leave the first call on Model")
	}
	// And a classification that did not clear the gate is no route.
	no := routableRoutedAgent(false)
	if _, _, ok := no.routeStep(func(Event) {}); ok {
		t.Fatal("a classification that did not clear the gate must not route")
	}
}

// The relaxation is for the first call only: the rest of the turn still
// has to earn each routed call with a clean mechanical step, and a
// tainted turn stops the route even on the first call.
func TestIntakeRouteRelaxesOnlyTheFirstCall(t *testing.T) {
	a := routableRoutedAgent(true)
	a.stepN = 2
	if _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("a later call with no mechanical step behind it must not route")
	}
	tainted := routableRoutedAgent(true)
	tainted.stepRoute.taint("a write")
	if _, _, ok := tainted.routeStep(func(Event) {}); ok {
		t.Fatal("a tainted turn must not route, first call or not")
	}
	later := routableRoutedAgent(true)
	later.turnN = 1
	if _, _, ok := later.routeStep(func(Event) {}); ok {
		t.Fatal("a later turn was never classified and must not route")
	}
}

// routingAgent wires a whole turn: one fake serving both the session's
// model and the cheap one, so the request the provider actually receives
// says which model the turn asked for.
func routingAgent(t *testing.T, fd *fakeDecisions) (*Agent, *fake) {
	t.Helper()
	f := &fake{responses: []*llm.Response{text("answered")}}
	a, _ := newAgent(t, f, nil)
	a.Log = &session.Memory{}
	a.Tools = tool.NewRegistry()
	a.Decisions = fd
	a.DecisionsIntake = true
	a.DecisionsIntakeRoute = true
	a.RouteCheap = "cheap-model"
	a.Route = func(model string) (llm.Provider, string, error) { return f, model, nil }
	return a, f
}

func TestIntakeRoutingSendsASmallReadOnlyTurnToTheCheapModel(t *testing.T) {
	fd := &fakeDecisions{res: routeResult("explain", 0.99, 0.3, 0.9, 0.2), avail: true}
	a, f := routingAgent(t, fd)
	if err := a.Run(context.Background(), "what does prepareBlocks do?", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(f.requests))
	}
	if f.requests[0].Model != "cheap-model" {
		t.Fatalf("first call went to %q, want cheap-model", f.requests[0].Model)
	}
	st := a.Stats()
	if st.RoutedCalls != 1 || st.DecisionActed != 1 {
		t.Fatalf("stats routed=%d acted=%d, want 1 and 1", st.RoutedCalls, st.DecisionActed)
	}
	extra := string(intakeEvents(a)[0].Extra)
	for _, want := range []string{`"route":"cheap"`, `"acted_on":true`, `"route_reason":"small read-only explain"`} {
		if !strings.Contains(extra, want) {
			t.Fatalf("intake Extra missing %s: %s", want, extra)
		}
	}
}

// The flag-off case, which has to be byte-identical to the feature not
// existing: same model, no routed call, and the record still says the
// classification was acted on by nobody.
func TestIntakeRoutingOffLeavesTheTurnOnTheSessionModel(t *testing.T) {
	fd := &fakeDecisions{res: routeResult("explain", 0.99, 0.3, 0.9, 0.2), avail: true}
	a, f := routingAgent(t, fd)
	a.DecisionsIntakeRoute = false
	if err := a.Run(context.Background(), "what does prepareBlocks do?", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].Model == "cheap-model" {
		t.Fatal("routing off must leave the first call on the session's model")
	}
	if st := a.Stats(); st.RoutedCalls != 0 || st.DecisionActed != 0 {
		t.Fatalf("stats routed=%d acted=%d, want 0 and 0", st.RoutedCalls, st.DecisionActed)
	}
	extra := string(intakeEvents(a)[0].Extra)
	if !strings.Contains(extra, `"acted_on":false`) || strings.Contains(extra, `"route":"`) {
		t.Fatalf("intake Extra should record no routing: %s", extra)
	}
}

// slowDecisions answers later than the routing deadline allows, which is
// the case the fail-open promise is about: the turn runs unrouted and
// the call is still collected, priced and logged at the end of it.
type slowDecisions struct {
	res   *jev.Result
	delay time.Duration
	calls int
}

func (s *slowDecisions) Evaluate(ctx context.Context, _ string, _ []jev.Question) (*jev.Result, error) {
	s.calls++
	select {
	case <-time.After(s.delay):
		return s.res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *slowDecisions) Available() bool { return true }

func TestIntakeRoutingDropsALateClassification(t *testing.T) {
	sd := &slowDecisions{res: routeResult("explain", 0.99, 0.3, 0.9, 0.2),
		delay: intakeRouteDeadline + 300*time.Millisecond}
	f := &fake{responses: []*llm.Response{text("answered")}}
	a, _ := newAgent(t, f, nil)
	a.Log = &session.Memory{}
	a.Tools = tool.NewRegistry()
	a.Decisions = sd
	a.DecisionsIntake, a.DecisionsIntakeRoute = true, true
	a.RouteCheap = "cheap-model"
	a.Route = func(model string) (llm.Provider, string, error) { return f, model, nil }
	if err := a.Run(context.Background(), "what does prepareBlocks do?", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if f.requests[0].Model == "cheap-model" {
		t.Fatal("a late classification must not route")
	}
	if st := a.Stats(); st.RoutedCalls != 0 {
		t.Fatalf("routed = %d, want 0", st.RoutedCalls)
	}
	// Collected at the end of the turn all the same: the call was paid
	// for, so it is counted and logged.
	if evs := intakeEvents(a); len(evs) != 1 {
		t.Fatalf("intake events = %d, want the late answer still logged", len(evs))
	}
	if st := a.Stats(); st.DecisionCalls != 1 || st.DecisionActed != 0 {
		t.Fatalf("stats = %+v", st)
	}
}
