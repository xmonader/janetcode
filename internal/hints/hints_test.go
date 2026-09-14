package hints

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNothingFiresOnAQuietSession(t *testing.T) {
	e := New(nil, nil)
	if got := e.Eval(&State{VerifyConfigured: true}); got != "" {
		t.Fatalf("a quiet session produced a hint: %q", got)
	}
}

func TestTheVerifyHintWaitsForTheThirdChangingTurn(t *testing.T) {
	e := New(nil, nil)
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
	e := New(nil, nil)
	got := e.Eval(&State{RepeatedBangCommand: "go test ./..."})
	if !strings.Contains(got, `/verify set "go test ./..."`) {
		t.Fatalf("hint did not offer the command the user ran twice: %q", got)
	}
}

func TestAConfiguredGateSilencesTheVerifyHint(t *testing.T) {
	e := New(nil, nil)
	if got := e.Eval(&State{MutatingTurns: 9, VerifyConfigured: true}); got != "" {
		t.Fatalf("hinted at a gate that is already configured: %q", got)
	}
}

func TestEachRuleFiresOnceOnly(t *testing.T) {
	e := New(nil, nil)
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
	e := New([]string{"verify-gate"}, nil)
	if got := e.Eval(&State{MutatingTurns: 9}); strings.Contains(got, "/verify set") {
		t.Fatalf("a hint recorded in a previous session fired again: %q", got)
	}
}

func TestOnlyOneHintPerEvaluation(t *testing.T) {
	e := New(nil, nil)
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
	e := New(nil, nil)
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
	e := New(nil, nil)
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
	e := New(nil, nil)
	s := &State{
		MutatingTurns: 9, ContextPct: 70, Spent: true,
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
	e := New(nil, nil)
	s := &State{VerifyConfigured: true, SlowCommandSeconds: 300}
	if got := e.Eval(s); strings.Contains(got, "ctrl+g") {
		t.Fatalf("offered a key that would do nothing: %q", got)
	}
}

func TestTheDenialHintNamesTheShapeAndTheCount(t *testing.T) {
	e := New(nil, nil)
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
		e := New(nil, nil)
		got := e.Eval(&State{VerifyConfigured: true, Untrusted: []string{tc.file}})
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s → %q, wanted %s", tc.file, got, tc.want)
		}
	}
}

func TestShownRoundTripsThroughTheStore(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, "proj-abc")
	if got, _ := s.Load(); got != nil {
		t.Fatalf("a fresh project already had %v", got)
	}
	if err := s.Save([]string{"verify-gate", "spend"}, []string{"budget"}); err != nil {
		t.Fatal(err)
	}
	shown, skipped := Open(dir, "proj-abc").Load()
	if len(shown) != 2 || shown[0] != "verify-gate" {
		t.Fatalf("read back %v", shown)
	}
	if len(skipped) != 1 || skipped[0] != "budget" {
		t.Fatalf("skipped read back %v", skipped)
	}
	// A different project has its own record.
	if got, _ := Open(dir, "other").Load(); got != nil {
		t.Fatalf("projects share a record: %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "hints", "proj-abc.json")); err != nil {
		t.Fatalf("record not where it belongs: %v", err)
	}
}

// A record written before skipped existed says nothing about it, and
// everything in it is a hint that was said — which is what the file
// meant when it was written.
func TestAStoreFileWithoutSkippedStillReads(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hints"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hints", "old.json"), []byte(`{"shown":["verify-gate"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	shown, skipped := Open(dir, "old").Load()
	if len(shown) != 1 || shown[0] != "verify-gate" || skipped != nil {
		t.Fatalf("old file read as %v / %v", shown, skipped)
	}
}

func TestTheStoreIsHarmlessWithNowhereToWrite(t *testing.T) {
	s := Open("", "")
	if shown, _ := s.Load(); shown != nil || s.Save([]string{"x"}, nil) != nil {
		t.Fatal("a store with no path misbehaved")
	}
}

// A rule spent silently suppresses the rule exactly like a shown one,
// but it is not a hint anybody read: /hints counts the two apart.
func TestSpentRulesAreNotCountedAsShown(t *testing.T) {
	e := New(nil, nil)
	e.MarkSpent("verify-gate")
	if !e.Given("verify-gate") {
		t.Fatal("a spent rule must still be suppressed")
	}
	if got := e.Shown(); len(got) != 0 {
		t.Fatalf("Shown = %v, want nothing said", got)
	}
	if got := e.Skipped(); len(got) != 1 || got[0] != "verify-gate" {
		t.Fatalf("Skipped = %v", got)
	}
	// The rule it stands for never fires again.
	if got := e.Eval(&State{MutatingTurns: 9}); got != "" {
		t.Fatalf("a spent rule fired: %q", got)
	}
	// A rule suppressed because the feature is already in use is skipped
	// too, not shown.
	e2 := New(nil, nil)
	if got := e2.Eval(&State{MutatingTurns: 9, Used: func(string) bool { return true }}); got != "" {
		t.Fatalf("a hint for a feature already in use: %q", got)
	}
	if len(e2.Shown()) != 0 || len(e2.Skipped()) == 0 {
		t.Fatalf("shown=%v skipped=%v", e2.Shown(), e2.Skipped())
	}
}

func TestFiringRecordsThroughTheEngine(t *testing.T) {
	e := New(nil, nil)
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
	e := New(nil, nil)
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
