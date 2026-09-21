package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func intakeResult(kind string, conf, complexity, needsPlan float64) *jev.Result {
	return &jev.Result{Model: "jev-1.13.0", TokensIn: 120, TokensOut: 40, Cost: 0.0000091,
		Answers: map[string]jev.Answer{
			qIntakeKind:       {Kind: jev.Choice, Choice: kind, Confidence: conf, Probabilities: map[string]float64{kind: conf}},
			qIntakeComplexity: {Kind: jev.Score, Score: complexity, Confidence: 0.6, Probabilities: map[string]float64{"3": 0.6}},
			qIntakeNeedsPlan:  {Kind: jev.Noul, Noul: needsPlan},
		}}
}

func intakeAgent(t *testing.T, fd *fakeDecisions) (*Agent, *fake) {
	t.Helper()
	f := &fake{responses: []*llm.Response{text("answered")}}
	a, _ := newAgent(t, f, nil)
	a.Log = &session.Memory{}
	a.Decisions = fd
	a.DecisionsIntake = true
	return a, f
}

func intakeEvents(a *Agent) []session.Event {
	var out []session.Event
	for _, e := range a.Log.Events() {
		if e.Type == session.TypeDecision && e.Name == "intake" {
			out = append(out, e)
		}
	}
	return out
}

// Phase 3a classifies the session's first prompt, logs it and counts
// it, and changes nothing the turn does.
func TestIntakeClassifiesTheFirstPromptAndOnlyLogsIt(t *testing.T) {
	fd := &fakeDecisions{res: intakeResult("debug", 0.82, 3.4, 0.71), avail: true}
	a, f := intakeAgent(t, fd)
	if err := a.Run(context.Background(), "the ratelimit test fails on CI but not here", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if fd.calls != 1 {
		t.Fatalf("intake calls = %d, want 1", fd.calls)
	}
	evs := intakeEvents(a)
	if len(evs) != 1 {
		t.Fatalf("intake events = %d, want 1", len(evs))
	}
	extra := string(evs[0].Extra)
	for _, want := range []string{`"consumer":"intake"`, `"kind":"debug"`, `"complexity":3.4`,
		`"needs_plan":0.71`, `"acted_on":false`, `"cost_usd"`, `"questions"`, `"answers"`} {
		if !strings.Contains(extra, want) {
			t.Fatalf("intake Extra missing %s: %s", want, extra)
		}
	}
	// Counted in stats, priced like every other decision call.
	st := a.Stats()
	if st.DecisionCalls != 1 || st.IntakeKind != "debug" || st.DecisionCost != 0.0000091 {
		t.Fatalf("stats = %+v", st)
	}
	if !strings.Contains(st.DecisionLine(), "first prompt read as debug") {
		t.Fatalf("DecisionLine = %q", st.DecisionLine())
	}
	// Log-only: the turn asked the model exactly what it would have
	// asked without the feature, and nothing was injected into it.
	if len(f.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(f.requests))
	}
	last := f.requests[0].Messages[len(f.requests[0].Messages)-1]
	for _, b := range last.Blocks {
		if strings.Contains(b.Text, "needs_plan") || strings.Contains(b.Text, "complexity") {
			t.Fatalf("intake leaked into the prompt: %q", b.Text)
		}
	}
}

// One classification per session: the second turn's prompt is not
// intake, and does not pay for another call.
func TestIntakeAsksOncePerSession(t *testing.T) {
	fd := &fakeDecisions{res: intakeResult("implement", 0.9, 2, 0.2), avail: true}
	a, _ := intakeAgent(t, fd)
	a.Settings().Provider.(*fake).responses = []*llm.Response{text("one"), text("two")}
	for _, p := range []string{"add a --json flag", "now document it"} {
		if err := a.Run(context.Background(), p, func(Event) {}); err != nil {
			t.Fatal(err)
		}
	}
	if fd.calls != 1 {
		t.Fatalf("intake calls = %d over two turns, want 1", fd.calls)
	}
	if len(intakeEvents(a)) != 1 {
		t.Fatalf("intake events = %d, want 1", len(intakeEvents(a)))
	}
}

// Every way the consumer can be absent or fail is the pre-feature
// session: no call, no event, no counter, no error.
func TestIntakeFailsOpen(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Agent, *fakeDecisions)
	}{
		{"flag off", func(a *Agent, _ *fakeDecisions) { a.DecisionsIntake = false }},
		{"no provider", func(a *Agent, _ *fakeDecisions) { a.Decisions = nil }},
		{"JANETCODE_OFF", func(a *Agent, _ *fakeDecisions) { a.Off = map[string]bool{"decisions": true} }},
		{"circuit open", func(_ *Agent, fd *fakeDecisions) { fd.avail = false }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fd := &fakeDecisions{res: intakeResult("docs", 0.9, 1, 0.1), avail: true}
			a, f := intakeAgent(t, fd)
			c.setup(a, fd)
			if err := a.Run(context.Background(), "write the README", func(Event) {}); err != nil {
				t.Fatal(err)
			}
			if fd.calls != 0 {
				t.Fatalf("calls = %d, want 0", fd.calls)
			}
			if len(intakeEvents(a)) != 0 || a.Stats().IntakeKind != "" {
				t.Fatal("a disabled consumer left a record")
			}
			if len(f.requests) != 1 {
				t.Fatalf("model calls = %d", len(f.requests))
			}
		})
	}
	// A provider that errors costs the session a counted failure and
	// nothing else — no event, no classification, no failed turn.
	fd := &fakeDecisions{err: errors.New("decisions 503: down"), avail: true}
	a, _ := intakeAgent(t, fd)
	if err := a.Run(context.Background(), "write the README", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(intakeEvents(a)) != 0 {
		t.Fatal("a failed call was logged as a classification")
	}
	if st := a.Stats(); st.DecisionErrors != 1 || st.IntakeKind != "" {
		t.Fatalf("stats after an error = %+v", st)
	}
}
