package agent

import (
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestRouteLineEmptyUntilSomethingRouted(t *testing.T) {
	var s Stats
	if line := s.RouteLine("haiku"); line != "" {
		t.Fatalf("nothing routed yet: RouteLine = %q", line)
	}
	s.RoutedCalls, s.ModelCalls = 1, 1
	if line := s.RouteLine(""); line != "" {
		t.Fatalf("no cheap model named: RouteLine = %q", line)
	}
}

func TestRouteLineReportsWhatActuallyRouted(t *testing.T) {
	s := Stats{RoutedCalls: 3, ModelCalls: 10}
	want := "routed 3 of 10 model calls to haiku"
	if got := s.RouteLine("haiku"); got != want {
		t.Fatalf("RouteLine = %q, want %q", got, want)
	}
}

func TestStatsCounterTracksPerModelCallsAndTokens(t *testing.T) {
	var c statsCounter
	c.model("main-model", "", 10*time.Millisecond, llm.Usage{InputTokens: 100, OutputTokens: 20}, false)
	c.model("main-model", "", 10*time.Millisecond, llm.Usage{InputTokens: 50, OutputTokens: 10}, false)
	c.model("cheap-model", "", 5*time.Millisecond, llm.Usage{InputTokens: 30, OutputTokens: 5}, false)
	c.model("cheap-model", "", 5*time.Millisecond, llm.Usage{}, true) // a failed call: no usage to add
	c.routed()
	c.routed()

	if c.s.ModelCalls != 4 {
		t.Fatalf("ModelCalls = %d, want 4", c.s.ModelCalls)
	}
	if c.s.ModelErrors != 1 {
		t.Fatalf("ModelErrors = %d, want 1", c.s.ModelErrors)
	}
	if c.s.RoutedCalls != 2 {
		t.Fatalf("RoutedCalls = %d, want 2", c.s.RoutedCalls)
	}
	byName := map[string]*ModelStat{}
	for _, m := range c.models {
		byName[m.Name] = m
	}
	main := byName["main-model"]
	if main == nil || main.Calls != 2 || main.Usage.InputTokens != 150 || main.Usage.OutputTokens != 30 {
		t.Fatalf("main-model = %+v", main)
	}
	cheap := byName["cheap-model"]
	if cheap == nil || cheap.Calls != 2 || cheap.Errors != 1 || cheap.Usage.InputTokens != 30 {
		t.Fatalf("cheap-model = %+v", cheap)
	}
}

// Stats() must surface the per-model breakdown sorted the same way Tools
// is: most calls first, name breaking ties.
func TestStatsSnapshotSortsModelsByCallsThenName(t *testing.T) {
	a := &Agent{}
	a.stats.model("b-model", "", time.Millisecond, llm.Usage{}, false)
	a.stats.model("a-model", "", time.Millisecond, llm.Usage{}, false)
	a.stats.model("a-model", "", time.Millisecond, llm.Usage{}, false)
	st := a.Stats()
	if len(st.Models) != 2 || st.Models[0].Name != "a-model" || st.Models[0].Calls != 2 {
		t.Fatalf("Models = %+v", st.Models)
	}
	if st.Models[1].Name != "b-model" || st.Models[1].Calls != 1 {
		t.Fatalf("Models = %+v", st.Models)
	}
}

// A gateway is free to answer with a different model than the one asked
// for, and an escalation that silently lands back on the cheap model is a
// measurement error. The served name is counted against the asked one.
func TestStatsRecordsTheModelTheProviderSaidAnswered(t *testing.T) {
	a := &Agent{}
	a.stats.model("strong", "strong", time.Millisecond, llm.Usage{}, false)
	a.stats.model("strong", "cheap", time.Millisecond, llm.Usage{}, false)
	a.stats.model("plain", "", time.Millisecond, llm.Usage{}, false)
	st := a.Stats()
	var strong, plain ModelStat
	for _, m := range st.Models {
		switch m.Name {
		case "strong":
			strong = m
		case "plain":
			plain = m
		}
	}
	if strong.Served["strong"] != 1 || strong.Served["cheap"] != 1 {
		t.Fatalf("Served = %v", strong.Served)
	}
	got, n := strong.ServedElsewhere()
	if got != "cheap" || n != 1 {
		t.Fatalf("ServedElsewhere = %q %d", got, n)
	}
	if plain.Served != nil {
		t.Fatalf("a provider that names nothing must record nothing: %v", plain.Served)
	}
	if got, n := plain.ServedElsewhere(); got != "" || n != 0 {
		t.Fatalf("ServedElsewhere on a silent provider = %q %d", got, n)
	}
}
