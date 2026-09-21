package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// escalatableAgent is an agent whose strong slot lives on another
// provider, which is the case the cheap step router refuses and the
// cascade must not.
func escalatableAgent() (*Agent, *fake) {
	strong := &fake{name: "openrouter"}
	a := &Agent{
		Provider:      &fake{name: "anthropic"},
		Tools:         tool.NewRegistry(),
		Model:         "base-model",
		StrongModel:   "strong-model",
		EscalateAfter: 1,
	}
	a.Route = func(model string) (llm.Provider, string, error) { return strong, model, nil }
	return a, strong
}

// Flag off is the whole default: no strong slot, or no round count, and
// nothing about a call changes.
func TestEscalationIsOffWithoutBothHalvesOfTheConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		breaks func(*Agent)
	}{
		{"no strong slot", func(a *Agent) { a.StrongModel = "" }},
		{"no round count", func(a *Agent) { a.EscalateAfter = 0 }},
		{"switched off", func(a *Agent) { a.Off = map[string]bool{"escalate": true} }},
		{"blank strong slot", func(a *Agent) { a.StrongModel = "   " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := escalatableAgent()
			tc.breaks(a)
			if a.EscalateEnabled() {
				t.Fatal("escalation should not be enabled")
			}
			evs, emit := collect()
			a.escalate("the gate failed", emit)
			if _, on := a.escalated(); on {
				t.Fatal("escalate must be a no-op when the feature is off")
			}
			if _, _, ok := a.escalateStep(emit); ok {
				t.Fatal("escalateStep must not claim a call")
			}
			if len(*evs) != 0 {
				t.Fatalf("a disabled feature must be silent: %+v", *evs)
			}
		})
	}
}

// Configured but untriggered is also byte-identical: the cascade only
// claims a call once a real check has failed.
func TestEscalationClaimsNoCallUntilItFires(t *testing.T) {
	a, _ := escalatableAgent()
	if !a.EscalateEnabled() {
		t.Fatal("both halves are set; escalation should be enabled")
	}
	evs, emit := collect()
	if _, _, ok := a.escalateStep(emit); ok {
		t.Fatal("nothing has failed yet")
	}
	if len(*evs) != 0 {
		t.Fatalf("an untriggered cascade must be silent: %+v", *evs)
	}
	a.escalate("the verification gate failed 1 round(s)", emit)
	prov, model, ok := a.escalateStep(emit)
	if !ok || model != "strong-model" || prov == nil || prov.Name() != "openrouter" {
		t.Fatalf("escalateStep = %v %q %v", prov, model, ok)
	}
	why, on := a.escalated()
	if !on || !strings.Contains(why, "gate failed") {
		t.Fatalf("escalated = %q %v", why, on)
	}
	// The notice fires once, naming the model and the reason.
	notices := 0
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "escalating to strong-model") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("expected one escalation notice, got %d: %+v", notices, *evs)
	}
	a.escalate("something else", emit)
	if why, _ := a.escalated(); why != "the verification gate failed 1 round(s)" {
		t.Fatalf("the first reason must stick: %q", why)
	}
}

// A cascade is per turn: the next request starts cheap again.
func TestEscalationClearsAtTheEndOfATurn(t *testing.T) {
	a, _ := escalatableAgent()
	a.escalate("the verification gate failed 1 round(s)", func(Event) {})
	a.endTurnEscalation()
	if _, on := a.escalated(); on {
		t.Fatal("the cascade must not survive its turn")
	}
	if _, _, ok := a.escalateStep(func(Event) {}); ok {
		t.Fatal("escalateStep must not claim the next turn's first call")
	}
}

// Escalation goes UP a tier, so routeCheapSmallContext -- which exists to
// stop a cold prompt cache being paid for a *cheaper* answer -- must not
// gate it. A turn that is failing its own checks is exactly when the
// switch is worth the cache miss.
func TestEscalationIgnoresTheCheapRouteContextCeiling(t *testing.T) {
	a, _ := escalatableAgent()
	a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, strings.Repeat("word ", routeCheapSmallContext)))
	if a.EstimateTokens() <= routeCheapSmallContext {
		t.Fatalf("the fixture is too small to test the ceiling: %d", a.EstimateTokens())
	}
	a.escalate("the verification gate failed 1 round(s)", func(Event) {})
	if _, model, ok := a.escalateStep(func(Event) {}); !ok || model != "strong-model" {
		t.Fatalf("a large conversation must still escalate: %q %v", model, ok)
	}
}

// A strong slot nothing can route turns the feature off for the session
// with one notice, rather than failing identically on every call.
func TestEscalationUnroutableStrongModelDisablesForTheSession(t *testing.T) {
	a, _ := escalatableAgent()
	calls := 0
	a.Route = func(model string) (llm.Provider, string, error) {
		calls++
		return nil, "", errStub("no provider serves it")
	}
	evs, emit := collect()
	a.escalate("the verification gate failed 1 round(s)", emit)
	if _, _, ok := a.escalateStep(emit); ok {
		t.Fatal("an unroutable strong model must not claim a call")
	}
	if _, _, ok := a.escalateStep(emit); ok {
		t.Fatal("still must not claim a call")
	}
	if calls != 1 {
		t.Fatalf("Route was asked %d times; the failure must be cached", calls)
	}
	found := false
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "escalation is off for this session") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no notice explained why escalation is off: %+v", *evs)
	}
}
