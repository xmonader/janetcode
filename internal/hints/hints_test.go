package hints

import (
	"encoding/json"
	"fmt"
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
	if !strings.Contains(got, `/verify set go test ./...`) {
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
		Promotable: true, BackgroundKey: "ctrl+g", SlowCommandSeconds: 90, ContextPct: 92,
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

// The trust hint exists to name a command. An untrusted entry nothing
// knows a command for used to produce a line ending in a bare colon,
// and to spend the rule on it.
func TestTheTrustHintNeedsACommandToName(t *testing.T) {
	e := New(nil, nil)
	if got := e.Eval(&State{VerifyConfigured: true, Untrusted: []string{"something-else"}}); got != "" {
		t.Fatalf("fired with no command to name: %q", got)
	}
	if e.Given("trust-project") {
		t.Fatal("the rule was spent on an entry it had nothing to say about")
	}
	// A recognised entry alongside it still fires, and the dangling
	// colon is gone.
	got := e.Eval(&State{VerifyConfigured: true, Untrusted: []string{"something-else", ".mcp.json"}})
	if !strings.Contains(got, "/mcp trust") || strings.HasSuffix(got, ": ") {
		t.Fatalf("hint = %q", got)
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

// A project key names one file inside the hints directory. One that
// would escape it writes nowhere rather than into somebody else's
// record.
func TestAKeyThatWouldEscapeTheHintsDirectoryWritesNothing(t *testing.T) {
	dir := t.TempDir()
	for _, key := range []string{"../escape", "a/b", `a\b`, "..", "."} {
		s := Open(dir, key)
		if err := s.Save([]string{"verify-gate"}, nil); err != nil {
			t.Fatalf("%q: %v", key, err)
		}
		if shown, _ := s.Load(); shown != nil {
			t.Fatalf("%q: stored %v", key, shown)
		}
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("wrote %v (err %v)", entries, err)
	}
}

// A record is replaced whole: the write lands on a temporary file and
// is renamed over the old one, so an interrupted save cannot leave a
// half-written record that reads as "nothing has been said".
func TestSavingReplacesTheRecordAtomically(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir, "proj")
	if err := s.Save([]string{"verify-gate"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Save([]string{"verify-gate", "spend"}, []string{"budget"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "hints"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "proj.json" {
		t.Fatalf("left %v behind", entries)
	}
	shown, skipped := s.Load()
	if len(shown) != 2 || len(skipped) != 1 {
		t.Fatalf("read back %v / %v", shown, skipped)
	}
}

// What comes back is a file on disk: it may be truncated, hand-edited
// or enormous, and none of that may end up in the engine's maps.
func TestAnImplausibleRecordIsBounded(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hints"), 0o700); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, maxIDs*3)
	ids = append(ids, "", "verify-gate", "")
	for i := 0; i < maxIDs*3; i++ {
		ids = append(ids, fmt.Sprintf("junk-%d", i))
	}
	data, err := json.Marshal(file{Shown: ids})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hints", "big.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	shown, _ := Open(dir, "big").Load()
	if len(shown) != maxIDs {
		t.Fatalf("loaded %d ids, want the %d-id bound", len(shown), maxIDs)
	}
	for _, id := range shown {
		if id == "" {
			t.Fatal("an empty id survived")
		}
	}
	// And the ids that were there are still honoured.
	if !New(shown, nil).Given("verify-gate") {
		t.Fatal("a real id was dropped by the bound")
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

// The hints for the features added since the engine: each fires exactly
// when its condition holds, is spent silently for a user who already uses
// what it teaches, and waits while the user is being asked something.
func TestTheFeatureHintsFireOnTheirMomentOnly(t *testing.T) {
	for _, tc := range []struct {
		id, use, want string
		on, off       State
	}{
		{"undo-about", "/undo about", "/undo about <description>", State{UndoneMixed: 2}, State{UndoneMixed: 1}},
		{"recipe-repeat", "/recipe save", "/recipe save <name>", State{RepeatedTurnShape: "edit_file VERSION, edit_file CHANGELOG"}, State{}},
		{"accept-tests", "/accept", "/accept <request>", State{TestFirstPrompt: true}, State{}},
		{"steer-or-queue", "ctrl+s", "ctrl+s instead hands", State{Queued: true}, State{}},
		{"eval-keep", "/eval keep", "/eval keep <name>", State{VerifiedAfterFailure: true}, State{}},
		{"proof-notes", "/proof", "/proof push", State{ProofAttached: true}, State{}},
		{"what-if-model", "what-if", "--what-if s1 --from-turn 1 --model m2", State{SwitchedModel: "m2", SessionID: "s1"}, State{SwitchedModel: "m2"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			quiet := func(s State) *State { s.VerifyConfigured = true; return &s }
			if got := New(nil, nil).Eval(quiet(tc.off)); got != "" {
				t.Fatalf("fired without its condition: %q", got)
			}
			e := New(nil, nil)
			blocked := quiet(tc.on)
			blocked.Blocked = true
			if got := e.Eval(blocked); got != "" {
				t.Fatalf("interrupted a prompt: %q", got)
			}
			got := e.Eval(quiet(tc.on))
			if !strings.Contains(got, tc.want) {
				t.Fatalf("hint = %q, want it to name %q", got, tc.want)
			}
			if e.Shown()[0] != tc.id {
				t.Fatalf("recorded %v", e.Shown())
			}
			if again := e.Eval(quiet(tc.on)); again != "" {
				t.Fatalf("fired twice: %q", again)
			}
			used := New(nil, nil)
			s := quiet(tc.on)
			s.Used = func(f string) bool { return f == tc.use }
			if got := used.Eval(s); got != "" {
				t.Fatalf("taught %s to a user who uses it: %q", tc.use, got)
			}
			if !used.Given(tc.id) {
				t.Fatal("the rule was not spent for a user who already uses the feature")
			}
		})
	}
}

// Having typed /undo says nothing about /undo about: the subcommand hints
// are suppressed by the subcommand, not by the command.
func TestASubcommandHintIsNotSuppressedByTheBareCommand(t *testing.T) {
	e := New(nil, nil)
	got := e.Eval(&State{VerifyConfigured: true, UndoneMixed: 3, Used: func(f string) bool { return f == "/undo" }})
	if !strings.Contains(got, "/undo about") {
		t.Fatalf("hint = %q", got)
	}
}

// The slow-command hint names the command and the key the user actually
// has: a rebound key is the one that works, and an unbound one leaves
// nothing to advise.
func TestSlowCommandHintNamesTheBoundKeyAndTheCommand(t *testing.T) {
	s := &State{VerifyConfigured: true, Promotable: true, SlowCommandSeconds: 45, SlowCommand: "go test ./...", BackgroundKey: "ctrl+b"}
	got := New(nil, nil).Eval(s)
	if !strings.Contains(got, "ctrl+b hands it") || strings.Contains(got, "ctrl+g") || !strings.Contains(got, "`go test ./...`") {
		t.Fatalf("hint = %q", got)
	}
	s.BackgroundKey = ""
	if got := New(nil, nil).Eval(s); strings.Contains(got, "background") {
		t.Fatalf("hinted at an unbound action: %q", got)
	}
}
