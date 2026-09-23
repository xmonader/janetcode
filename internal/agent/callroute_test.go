package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// The routing evidence contract (github issue #4): every model call in the
// session log names the slot, provider, model and reason that put it where
// it ran — main calls included, which are reported as no route.

// TestRunRecordsRouteEvidence drives a real two-step turn whose first step
// is a clean read and asserts the assistant events carry the evidence: the
// first call is main's (an empty Route says "no detour"), the second is the
// cheap slot's, with the policy's reason in its own words.
func TestRunRecordsRouteEvidence(t *testing.T) {
	main := &fake{name: "anthropic", responses: []*llm.Response{
		toolCall("t1", "read_file", `{"path":"a.txt"}`),
	}}
	cheap := &fake{name: "openrouter", responses: []*llm.Response{
		text("it says alpha"),
	}}
	a, _ := newAgent(t, main, nil)
	a.RouteCheap = "cheap-model"
	a.Route = func(model string) (llm.Provider, string, error) { return cheap, model, nil }
	mem := &session.Memory{}
	a.Log = mem

	if err := a.Run(context.Background(), "read a.txt", func(Event) {}); err != nil {
		t.Fatal(err)
	}

	var step1, step2 *session.Event
	for i := range mem.Events() {
		e := &mem.Events()[i]
		if e.Type != session.TypeAssistant {
			continue
		}
		if e.Step == 1 {
			step1 = e
		}
		if e.Step == 2 {
			step2 = e
		}
	}
	if step1 == nil || step2 == nil {
		t.Fatalf("assistant events: step1=%+v step2=%+v", step1, step2)
	}

	// The session's own model: a slot name, a provider, and no route —
	// "no detour" is reported as empty, not as a kind of its own.
	if step1.Slot != "main" || step1.Provider != "anthropic" || step1.Route != "" || step1.RouteWhy != "" {
		t.Fatalf("step 1 evidence = slot %q provider %q route %q why %q", step1.Slot, step1.Provider, step1.Route, step1.RouteWhy)
	}

	// The routed step: cheap slot, cheap provider, the route kind, and a
	// reason a reader can check against the policy in steproute.go.
	if step2.Model != "cheap-model" || step2.Slot != "cheap" || step2.Provider != "openrouter" {
		t.Fatalf("step 2 evidence = model %q slot %q provider %q", step2.Model, step2.Slot, step2.Provider)
	}
	if step2.Route != "route" {
		t.Fatalf("step 2 route kind = %q", step2.Route)
	}
	if !strings.Contains(step2.RouteWhy, "mechanical") {
		t.Fatalf("step 2 route why = %q", step2.RouteWhy)
	}
	if len(cheap.requests) != 1 {
		t.Fatalf("cheap provider saw %d requests, want 1", len(cheap.requests))
	}
}

// A cheap slot that resolves to the session's own model is not a route
// (config.SlotIsMain is the config-side spelling; routeStep refuses it
// before any evidence is written), so its call reads exactly like main's.
func TestMainModelCheapSlotIsNotARoute(t *testing.T) {
	main := &fake{name: "anthropic", responses: []*llm.Response{
		toolCall("t1", "read_file", `{"path":"a.txt"}`),
		text("it says alpha"),
	}}
	a, _ := newAgent(t, main, nil)
	a.RouteCheap = "m" // the session's own model id
	mem := &session.Memory{}
	a.Log = mem

	if err := a.Run(context.Background(), "read a.txt", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, e := range mem.Events() {
		if e.Type == session.TypeAssistant && (e.Route != "" || e.Slot != "main") {
			t.Fatalf("slot==main must report no route, got slot %q route %q", e.Slot, e.Route)
		}
	}
}

// /routing veto: ToggleOff flips the switch RoutingOff reads, and a vetoed
// session never routes.
func TestRoutingToggleVetoesStepRouting(t *testing.T) {
	a := routableAgent(true)
	if a.RoutingOff() {
		t.Fatal("routing starts on")
	}
	if !a.ToggleOff("routing") {
		t.Fatal("first toggle should veto routing")
	}
	if !a.RoutingOff() {
		t.Fatal("RoutingOff must see the veto")
	}
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("a vetoed session must not route")
	}
	if a.ToggleOff("routing") {
		t.Fatal("second toggle should lift the veto")
	}
	if _, _, _, ok := a.routeStep(func(Event) {}); !ok {
		t.Fatal("routing must return once the veto lifts")
	}
}

// routeStep's reason names the branch that said yes, in the policy's own
// words: a mid-turn route is earned by the step before it.
func TestRouteStepReportsTheMechanicalReason(t *testing.T) {
	a := routableAgent(true)
	_, _, why, ok := a.routeStep(func(Event) {})
	if !ok {
		t.Fatal("routableAgent should route")
	}
	if why != "the previous step was a clean, mechanical batch" {
		t.Fatalf("why = %q", why)
	}
}
