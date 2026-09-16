package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// Agent.Route may answer with no provider, meaning "the one already in
// use serves that model" — sideQuery has always read it that way. The
// fallback chain and the step router took it literally and would have
// sent the call to a nil provider.

func TestFallbackSurvivesARouterWithNoProvider(t *testing.T) {
	f := &fake{
		errs:      []error{errors.New("429 rate limit exceeded")},
		responses: []*llm.Response{nil, text("done")},
	}
	a, _ := newAgent(t, f, nil)
	a.Fallbacks = []string{"spare"}
	a.Route = func(model string) (llm.Provider, string, error) { return nil, model, nil }
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("%d requests, want the failed call and the fallback", len(f.requests))
	}
	if f.requests[1].Model != "spare" {
		t.Fatalf("fallback went to %q, want the spare on the session's own provider", f.requests[1].Model)
	}
}

func TestStepRoutingIsOffWhenTheRouterNamesNoProvider(t *testing.T) {
	r := &modelRouter{RouteCheap: "cheap"}
	r.Route = func(string) (llm.Provider, string, error) { return nil, "cheap", nil }
	var notices []string
	emit := func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}
	// With no session provider either there is nothing to send to, so
	// routing turns itself off rather than resolving to nil.
	if _, _, _, ok := r.resolveCheap(nil, emit); ok {
		t.Fatal("step routing stayed on with no provider to route to")
	}
	if len(notices) == 0 {
		t.Fatal("nothing said why step routing is off")
	}
	// With one, the cheap model rides on it.
	r2 := &modelRouter{RouteCheap: "cheap"}
	r2.Route = func(string) (llm.Provider, string, error) { return nil, "cheap", nil }
	cur := &fake{name: "session"}
	prov, model, same, ok := r2.resolveCheap(cur, func(Event) {})
	if !ok || prov == nil || model != "cheap" || !same {
		t.Fatalf("resolveCheap = (%v, %q, %v, %v)", prov, model, same, ok)
	}
}
