package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func mechUse(name, input string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, Name: name, Input: json.RawMessage(input)}
}

func mechResult(isErr bool) llm.Block {
	return llm.Block{Type: llm.BlockToolResult, IsError: isErr}
}

func TestStepMechanicalRecognisesReadsAndPassingChecks(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
		isErr bool
		want  bool
	}{
		{"read_file is mechanical", "read_file", `{"path":"a.go"}`, false, true},
		{"grep is mechanical", "grep", `{"pattern":"x"}`, false, true},
		{"a failed read is not", "read_file", `{"path":"a.go"}`, true, false},
		{"a passing check is mechanical", "bash", `{"command":"go test ./..."}`, false, true},
		{"a failing check is not", "bash", `{"command":"go test ./..."}`, true, false},
		{"a non-check bash call is not", "bash", `{"command":"echo hi"}`, false, false},
		{"a write is never mechanical", "write_file", `{"path":"a.go","content":"x"}`, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stepMechanical(c.tool, []byte(c.input), c.isErr); got != c.want {
				t.Errorf("stepMechanical(%s) = %v, want %v", c.tool, got, c.want)
			}
		})
	}
}

func TestAllMechanicalStepRequiresACleanAllMechanicalBatch(t *testing.T) {
	// An empty batch trusts nothing: there is nothing to have gone well.
	if allMechanicalStep(nil, nil) {
		t.Fatal("an empty step should not be mechanical")
	}
	// One mechanical call, all clean.
	uses := []llm.Block{mechUse("read_file", `{"path":"a.go"}`), mechUse("grep", `{"pattern":"x"}`)}
	results := []llm.Block{mechResult(false), mechResult(false)}
	if !allMechanicalStep(uses, results) {
		t.Fatal("two clean mechanical calls should be a mechanical step")
	}
	// The rule this test is named for: one failed call anywhere in the
	// batch, even a mechanical tool, disqualifies the whole step.
	failed := []llm.Block{mechResult(false), mechResult(true)}
	if allMechanicalStep(uses, failed) {
		t.Fatal("a step with a failed call should not be mechanical")
	}
	// A mutating (non-mechanical) tool in the batch disqualifies it too,
	// even though it succeeded.
	mixed := []llm.Block{mechUse("read_file", `{"path":"a.go"}`), mechUse("write_file", `{"path":"a.go"}`)}
	mixedResults := []llm.Block{mechResult(false), mechResult(false)}
	if allMechanicalStep(mixed, mixedResults) {
		t.Fatal("a step with a non-mechanical call should not be mechanical")
	}
}

// routableAgent builds a minimal Agent wired for routeStep's rules: two
// distinct fake providers (so the same-provider cache rule can go either
// way), RouteCheap configured and resolvable, stepN past the first call,
// and the previous step recorded as a clean mechanical batch. Each test
// then unsets exactly the one thing it means to test.
func routableAgent(sameProvider bool) *Agent {
	main := &fake{name: "anthropic"}
	cheapName := "anthropic"
	if !sameProvider {
		cheapName = "openrouter"
	}
	cheap := &fake{name: cheapName}
	a := &Agent{
		Provider: main,
		Tools:    tool.NewRegistry(),
		Model:    "main-model",
		stepN:    2,
	}
	a.RouteCheap = "cheap-model"
	a.Route = func(model string) (llm.Provider, string, error) {
		return cheap, model, nil
	}
	a.stepRoute.prevMechanical = true
	return a
}

func TestRouteStepFirstCallOfTurnNeverRoutes(t *testing.T) {
	a := routableAgent(true)
	a.stepN = 1
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("the first model call of a turn must stay on Model")
	}
}

func TestRouteStepRoutesAMechanicalStreak(t *testing.T) {
	a := routableAgent(true)
	prov, model, _, ok := a.routeStep(func(Event) {})
	if !ok {
		t.Fatal("a clean mechanical previous step should route")
	}
	if model != "cheap-model" {
		t.Fatalf("model = %q", model)
	}
	if prov == nil {
		t.Fatal("no provider returned")
	}
}

func TestRouteStepDoesNotRouteAfterAFailure(t *testing.T) {
	a := routableAgent(true)
	a.stepRoute.prevMechanical = false // the previous step had a failed or non-mechanical call
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("a step following a failure or a non-mechanical call must not route")
	}
}

func TestRouteStepDoesNotRouteAfterAGateFailure(t *testing.T) {
	a := routableAgent(true)
	a.stepRoute.taint("a verification check failed and was sent back")
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("a turn tainted by a gate failure must stay on Model for the rest of the turn")
	}
	// Tainted for the *rest* of the turn, not just the very next call: a
	// later mechanical streak must not un-taint it.
	a.stepRoute.prevMechanical = true
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("a later mechanical step must not clear an earlier taint")
	}
}

func TestRouteStepDoesNotRouteAfterUserSteering(t *testing.T) {
	a := routableAgent(true)
	evs, emit := collect()
	a.steer = []string{"do it differently"}
	a.deliverSteering(emit)
	_ = evs
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("a turn the user steered must stay on Model")
	}
}

func TestRouteStepDoesNotRouteAfterACriticWarning(t *testing.T) {
	a := routableAgent(true)
	a.warned = true
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("a turn the critic warned about must stay on Model")
	}
}

func TestRouteStepNeverRoutesInPlanMode(t *testing.T) {
	a := routableAgent(true)
	a.PlanMode = true
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("plan mode must never route")
	}
}

func TestRouteStepOffByDefault(t *testing.T) {
	a := routableAgent(true)
	a.RouteCheap = ""
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("an empty RouteCheap must never route")
	}
}

func TestRouteStepUnresolvableModelDisablesForTheSession(t *testing.T) {
	a := routableAgent(true)
	a.Route = func(model string) (llm.Provider, string, error) {
		return nil, "", errStub("no provider serves it")
	}
	evs, emit := collect()
	if _, _, _, ok := a.routeStep(emit); ok {
		t.Fatal("a model nothing can route must not route")
	}
	found := false
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "step routing is off for this session") {
			found = true
		}
	}
	if !found {
		t.Fatal("no notice explained why routing is off")
	}
	// The failure is cached: a second attempt must not call Route again,
	// or emit the notice a second time.
	*evs = nil
	a.Route = func(model string) (llm.Provider, string, error) {
		t.Fatal("Route should not be called again once disabled")
		return nil, "", nil
	}
	if _, _, _, ok := a.routeStep(emit); ok {
		t.Fatal("still should not route")
	}
	if len(*evs) != 0 {
		t.Fatalf("the notice fired again: %+v", *evs)
	}
}

// The cache rule (see steproute.go): a cross-provider route needs a small
// enough conversation that repricing it from cold costs little; a
// same-provider route is trusted regardless of size.
func TestRouteStepCacheRuleGatesCrossProviderOnContextSize(t *testing.T) {
	a := routableAgent(false) // cheap model lives on a different provider
	if _, _, _, ok := a.routeStep(func(Event) {}); !ok {
		t.Fatal("a small conversation should route across providers")
	}
	// Force EstimateTokens over the threshold with a long message.
	a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, strings.Repeat("word ", routeCheapSmallContext)))
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("a large conversation must not switch providers")
	}
}

func TestRouteStepSameProviderRoutesRegardlessOfContextSize(t *testing.T) {
	a := routableAgent(true)
	a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, strings.Repeat("word ", routeCheapSmallContext)))
	if _, _, _, ok := a.routeStep(func(Event) {}); !ok {
		t.Fatal("a same-provider route should not care how large the conversation is")
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }

// A cheap slot filled with the session's own model is the fallback said
// out loud, not a route: switching a call from a model to itself changes
// nothing, and counting it as a route would put a line in /stats about
// work that never happened.
func TestRouteStepDoesNotRouteToTheSessionsOwnModel(t *testing.T) {
	a := routableAgent(true)
	a.SetRouteCheap(a.Settings().Model)
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("cheap == main must not route")
	}
	// Case is not the difference between two models either.
	a.SetRouteCheap(strings.ToUpper(a.Settings().Model))
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("cheap == main must not route whatever the case")
	}
	// And a genuinely different name still routes, so the guard is the
	// equality and nothing else.
	a.SetRouteCheap("cheap-model")
	if _, model, _, ok := a.routeStep(func(Event) {}); !ok || model != "cheap-model" {
		t.Fatalf("a different cheap model must still route: ok=%v model=%q", ok, model)
	}
}

// The cache rule compares the cheap slot's provider with the one the
// session is on now, not the one it was on when route_cheap was first
// resolved: a fallback or a /model switch that moves the session to
// another backend makes a route to the old one a cross-provider route,
// and a large conversation must not be re-warmed from cold for it.
func TestRouteStepCacheRuleFollowsTheSessionsCurrentProvider(t *testing.T) {
	a := routableAgent(true)
	if _, _, _, ok := a.routeStep(func(Event) {}); !ok {
		t.Fatal("a same-provider route should resolve and route")
	}
	a.UpdateSettings(func(s *Settings) { s.Provider = &fake{name: "deepseek"} })
	a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, strings.Repeat("word ", routeCheapSmallContext)))
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("the session moved providers; a large conversation must not route back to the old one")
	}
}
