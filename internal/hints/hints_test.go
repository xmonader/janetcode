package hints

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNothingFiresOnAQuietSession(t *testing.T) {
	e := New(nil)
	if got := e.Eval(&State{VerifyConfigured: true}); got != "" {
		t.Fatalf("a quiet session produced a hint: %q", got)
	}
}

func TestTheVerifyHintWaitsForTheThirdChangingTurn(t *testing.T) {
	e := New(nil)
	s := &State{}
	for s.MutatingTurns = 1; s.MutatingTurns < 3; s.MutatingTurns++ {
		if got := e.Eval(s); got != "" {
			t.Fatalf("fired on turn %d: %q", s.MutatingTurns, got)
		}
	}
	got := e.Eval(s)
	if !strings.Contains(got, "/verify set") {
		t.Fatalf("third changing turn said %q", got)
	}
}

func TestRunningTheSameTestCommandTwiceIsEnough(t *testing.T) {
	e := New(nil)
	got := e.Eval(&State{RepeatedBangCommand: "go test ./..."})
	if !strings.Contains(got, `/verify set "go test ./..."`) {
		t.Fatalf("hint did not offer the command the user ran twice: %q", got)
	}
}

func TestAConfiguredGateSilencesTheVerifyHint(t *testing.T) {
	e := New(nil)
	if got := e.Eval(&State{MutatingTurns: 9, VerifyConfigured: true}); got != "" {
		t.Fatalf("hinted at a gate that is already configured: %q", got)
	}
}

func TestEachRuleFiresOnceOnly(t *testing.T) {
	e := New(nil)
	s := &State{MutatingTurns: 5}
	first := e.Eval(s)
	if first == "" {
		t.Fatal("nothing fired")
	}
	if again := e.Eval(s); again == first {
		t.Fatalf("the same hint fired twice: %q", again)
	}
}

func TestAnIdAlreadyShownNeverFiresAgain(t *testing.T) {
	e := New([]string{"verify-gate"})
	if got := e.Eval(&State{MutatingTurns: 9}); strings.Contains(got, "/verify set") {
		t.Fatalf("a hint recorded in a previous session fired again: %q", got)
	}
}

func TestOnlyOneHintPerEvaluation(t *testing.T) {
	e := New(nil)
	// Every rule at once: a user who has done everything wrong should
	// still get one line, not nine.
	s := &State{
		MutatingTurns: 9, DeniedShape: "bash(rm *)", DeniedCount: 4,
		Promotable: true, SlowCommandSeconds: 90, ContextPct: 92,
		PasteLines: 200, CostUSD: 4, Untrusted: []string{".mcp.json"},
		UnpricedModel: "who/knows", ResumedAfterCompaction: true,
	}
	got := e.Eval(s)
	if got == "" || strings.Count(got, "\n") > 0 {
		t.Fatalf("expected exactly one line, got %q", got)
	}
}

func TestNothingFiresWhileTheUserIsBeingAsked(t *testing.T) {
	e := New(nil)
	s := &State{MutatingTurns: 9, Blocked: true}
	if got := e.Eval(s); got != "" {
		t.Fatalf("interrupted a permission prompt with %q", got)
	}
	// And the rule is not spent: it fires once the prompt is gone.
	s.Blocked = false
	if got := e.Eval(s); got == "" {
		t.Fatal("the hint was lost rather than deferred")
	}
}

func TestOffSilencesEverything(t *testing.T) {
	e := New(nil)
	e.Off = true
	s := &State{MutatingTurns: 9, ContextPct: 99, CostUSD: 10}
	if got := e.Eval(s); got != "" {
		t.Fatalf("hints are off but %q was produced", got)
	}
	// And nothing was spent, so turning them back on still works.
	e.Off = false
	if got := e.Eval(s); got == "" {
		t.Fatal("turning hints back on produced nothing")
	}
}

func TestAFeatureTheUserKnowsIsNotIntroduced(t *testing.T) {
	e := New(nil)
	s := &State{
		MutatingTurns: 9, ContextPct: 70,
		Used: func(f string) bool { return f == "/verify" },
	}
	got := e.Eval(s)
	if strings.Contains(got, "/verify set") {
		t.Fatalf("introduced a feature the user already uses: %q", got)
	}
	if !strings.Contains(got, "/compact") {
		t.Fatalf("expected the next applicable hint, got %q", got)
	}
}

func TestCtrlGIsNotOfferedWhenThereIsNothingToPromote(t *testing.T) {
	e := New(nil)
	s := &State{VerifyConfigured: true, SlowCommandSeconds: 300}
	if got := e.Eval(s); strings.Contains(got, "ctrl+g") {
		t.Fatalf("offered a key that would do nothing: %q", got)
	}
}

func TestTheDenialHintNamesTheShapeAndTheCount(t *testing.T) {
	e := New(nil)
	got := e.Eval(&State{VerifyConfigured: true, DeniedShape: "bash(git push *)", DeniedCount: 3})
	if !strings.Contains(got, "bash(git push *)") || !strings.Contains(got, "3rd") {
		t.Fatalf("hint = %q", got)
	}
}

func TestTrustHintNamesTheRightCommand(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{".mcp.json", "/mcp trust"},
		{".janetcode/hooks.json", "/hooks trust"},
		{".janetcode/skills", "/skills"},
	} {
		e := New(nil)
		got := e.Eval(&State{VerifyConfigured: true, Untrusted: []string{tc.file}})
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s → %q, wanted %s", tc.file, got, tc.want)
		}
	}
}

func TestShownRoundTripsThroughTheStore(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, "proj-abc")
	if got := s.Shown(); got != nil {
		t.Fatalf("a fresh project already had %v", got)
	}
	if err := s.Save([]string{"verify-gate", "spend"}); err != nil {
		t.Fatal(err)
	}
	if got := Open(dir, "proj-abc").Shown(); len(got) != 2 || got[0] != "verify-gate" {
		t.Fatalf("read back %v", got)
	}
	// A different project has its own record.
	if got := Open(dir, "other").Shown(); got != nil {
		t.Fatalf("projects share a record: %v", got)
	}
	if want := filepath.Join(dir, "hints", "proj-abc.json"); s.Path() != want {
		t.Fatalf("path = %q", s.Path())
	}
}

func TestTheStoreIsHarmlessWithNowhereToWrite(t *testing.T) {
	s := Open("", "")
	if s.Shown() != nil || s.Save([]string{"x"}) != nil || s.Path() != "" {
		t.Fatal("a store with no path misbehaved")
	}
}

func TestFiringRecordsThroughTheEngine(t *testing.T) {
	e := New(nil)
	var recorded []string
	e.Record = func(id string) { recorded = append(recorded, id) }
	e.Eval(&State{MutatingTurns: 9})
	if len(recorded) != 1 || recorded[0] != "verify-gate" {
		t.Fatalf("recorded %v", recorded)
	}
	if got := e.Shown(); len(got) != 1 || got[0] != "verify-gate" {
		t.Fatalf("Shown = %v", got)
	}
}

func TestEveryRuleHasAnIdAndSomethingToSay(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Rules() {
		if r.ID == "" || r.When == nil || r.Text == nil {
			t.Errorf("incomplete rule %q", r.ID)
		}
		if seen[r.ID] {
			t.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
	}
	if len(seen) < 9 {
		t.Errorf("only %d rules", len(seen))
	}
}

// The hint used to fire at startup, on the strength of the price table
// alone, and told a user of a provider that reports its own costs that
// "the cost figures will be blank rather than wrong" while the status
// line showed a cost. It waits for a call that produced no figure.
func TestTheUnpricedHintWaitsToSeeWhetherTheFiguresAreActuallyBlank(t *testing.T) {
	e := New(nil)
	if got := e.Eval(&State{UnpricedModel: "x/y"}); got != "" {
		t.Fatalf("fired before a single call: %q", got)
	}
	if got := e.Eval(&State{UnpricedModel: "x/y", Spent: true, CostKnown: true}); got != "" {
		t.Fatalf("fired although the provider reported a cost: %q", got)
	}
	got := e.Eval(&State{UnpricedModel: "x/y", Spent: true})
	if !strings.Contains(got, "x/y") {
		t.Fatalf("did not fire once the figures really were blank: %q", got)
	}
}

func TestANilEngineIsSilentRatherThanFatal(t *testing.T) {
	var e *Engine
	if got := e.Eval(&State{MutatingTurns: 99}); got != "" {
		t.Fatalf("nil engine produced %q", got)
	}
}
