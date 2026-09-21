package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func advisableAgent(responses ...*llm.Response) *Agent {
	a := &Agent{
		Provider:    &fake{name: "anthropic", responses: responses},
		Tools:       tool.NewRegistry(),
		Model:       "base-model",
		AdvisorMode: AdvisorAlways,
	}
	a.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, "make the tests pass")}
	return a
}

// Off is the default and is byte-identical to the feature not existing:
// no consult, no message appended, no provider call.
func TestForcedAdvisorIsOffByDefault(t *testing.T) {
	for _, mode := range []string{"", AdvisorOff, "  ", "gated"} {
		a := advisableAgent()
		a.AdvisorMode = mode
		before := len(a.Messages)
		if a.forceAdvice(context.Background(), "turn start", "advise", func(Event) {}) {
			t.Fatalf("mode %q consulted", mode)
		}
		if len(a.Messages) != before {
			t.Fatalf("mode %q changed the history", mode)
		}
		if a.Stats().AdvisorConsults != 0 {
			t.Fatalf("mode %q counted a consult", mode)
		}
	}
	// The behaviour switch turns a configured advisor off too.
	a := advisableAgent(text("advice"))
	a.Off = map[string]bool{"advisor": true}
	if a.forceAdvice(context.Background(), "turn start", "advise", func(Event) {}) {
		t.Fatal("--eval-off advisor must silence it")
	}
}

// On, the advice lands in the executor's context as harness text and is
// counted, and the notice names the trigger.
func TestForcedAdvisorInjectsAdviceAndCountsIt(t *testing.T) {
	a := advisableAgent(text("do the occurs check"))
	evs, emit := collect()
	if !a.forceAdvice(context.Background(), "gate failure", "what is wrong?", emit) {
		t.Fatal("a configured advisor should have consulted")
	}
	last := a.Messages[len(a.Messages)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text(), "do the occurs check") {
		t.Fatalf("advice did not reach the history: %+v", last)
	}
	if !strings.Contains(last.Text(), "no tools and did not act") {
		t.Fatalf("the advice is not framed as advice: %q", last.Text())
	}
	if a.Stats().AdvisorConsults != 1 {
		t.Fatalf("AdvisorConsults = %d", a.Stats().AdvisorConsults)
	}
	found := false
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "advisor consulted (gate failure)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no notice named the trigger: %+v", *evs)
	}
}

// The cap is per turn, and startTurn refills it.
func TestForcedAdvisorCapsConsultsPerTurn(t *testing.T) {
	a := advisableAgent(text("one"), text("two"), text("three"))
	ctx := context.Background()
	if !a.forceAdvice(ctx, "turn start", "q", func(Event) {}) {
		t.Fatal("first consult refused")
	}
	if !a.forceAdvice(ctx, "gate failure", "q", func(Event) {}) {
		t.Fatal("second consult refused")
	}
	if a.forceAdvice(ctx, "gate failure", "q", func(Event) {}) {
		t.Fatal("a third consult passed the cap of two")
	}
	if a.Stats().AdvisorConsults != 2 {
		t.Fatalf("AdvisorConsults = %d", a.Stats().AdvisorConsults)
	}
	a.startTurn()
	if a.advisorForcedBudget() != advisorForcedCap {
		t.Fatalf("the budget did not refill: %d", a.advisorForcedBudget())
	}
	// An explicit cap is honoured over the default.
	b := advisableAgent(text("one"), text("two"))
	b.AdvisorMaxConsults = 1
	if !b.forceAdvice(ctx, "turn start", "q", func(Event) {}) {
		t.Fatal("first consult refused")
	}
	if b.forceAdvice(ctx, "gate failure", "q", func(Event) {}) {
		t.Fatal("a second consult passed a cap of one")
	}
}

// Fail open: a provider error and an empty reply both leave the
// conversation exactly as it was, and the budget is not refunded -- a
// failing advisor must not be retried until the turn ends.
func TestForcedAdvisorFailsOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    *Agent
	}{
		{"provider error", func() *Agent {
			a := advisableAgent()
			a.Provider = &fake{name: "anthropic", errs: []error{errStub("upstream is down")}}
			return a
		}()},
		{"empty reply", advisableAgent(text("   "))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(tc.a.Messages)
			if tc.a.forceAdvice(context.Background(), "turn start", "q", func(Event) {}) {
				t.Fatal("a failed consult must not report success")
			}
			if len(tc.a.Messages) != before {
				t.Fatal("a failed consult must not change the history")
			}
			if tc.a.Stats().AdvisorConsults != 0 {
				t.Fatal("a failed consult must not be counted as one")
			}
			if tc.a.advisorForcedBudget() != advisorForcedCap-1 {
				t.Fatalf("a failed consult must still spend its budget: %d", tc.a.advisorForcedBudget())
			}
		})
	}
}

// A cancelled turn buys nothing.
func TestForcedAdvisorRefusesACancelledTurn(t *testing.T) {
	a := advisableAgent(text("advice"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if a.forceAdvice(ctx, "turn start", "q", func(Event) {}) {
		t.Fatal("a cancelled turn must not buy a consult")
	}
}

// An empty request has nothing to advise on.
func TestForcedAdvisorSkipsAnEmptyRequest(t *testing.T) {
	a := advisableAgent(text("advice"))
	a.adviseAtTurnStart(context.Background(), "   ", func(Event) {})
	if a.Stats().AdvisorConsults != 0 {
		t.Fatal("an empty request must not buy a consult")
	}
}
