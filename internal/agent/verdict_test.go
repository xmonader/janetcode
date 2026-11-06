package agent

import (
	"context"
	"github.com/xmonader/janetcode/internal/shell"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// result builds one tool/result event of a turn.
func result(turn int, name, args, out string, isErr bool) session.Event {
	return session.Event{Type: session.TypeToolResult, Turn: turn, CallID: "c" + name + args, Name: name, Arguments: args, Output: out, IsError: isErr}
}

func say(turn int, text string) session.Event {
	m := llm.TextMessage(llm.RoleAssistant, text)
	return session.Event{Type: session.TypeAssistant, Turn: turn, Surface: true, Message: &m}
}

func TestVerdicts(t *testing.T) {
	edit := `{"path":"a.go","old_str":"x","new_str":"y"}`
	test := `{"command":"go test ./..."}`

	cases := []struct {
		name   string
		events []session.Event
		want   Verdict
	}{
		{"nothing happened", nil, VerdictNoChanges},
		{"only read", []session.Event{result(1, "read_file", `{"path":"a.go"}`, "1\tx", false)}, VerdictNoChanges},
		{"edited, ran nothing", []session.Event{result(1, "edit_file", edit, "ok", false)}, VerdictUnverified},
		{"edited, check red", []session.Event{
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", test, "FAIL\n[exit code 1]", true),
		}, VerdictCheckFailed},
		{"edited, tests ran and passed", []session.Event{
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", test, "ok  \texample.com/x\t0.02s\n[exit code 0]", false),
		}, VerdictVerified},
		{"edited, only a build passed", []session.Event{
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", `{"command":"go build ./..."}`, "\n[exit code 0]", false),
		}, VerdictNotDemonstrated},
		{"go run where one package has no test files but another passes", []session.Event{
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", test, "ok  \texample.com/cfg\t0.002s\n?   \texample.com/cfg/cmd/app\t[no test files]\n[exit code 0]", false),
		}, VerdictVerified},
		{"pytest that collected nothing", []session.Event{
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", `{"command":"pytest tests/"}`, "collected 0 items\n\nno tests ran in 0.01s\n[exit code 0]", false),
		}, VerdictVacuous},
		{"edited, check went red to green", []session.Event{
			result(0, "bash", test, "FAIL\n[exit code 1]", true),
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", test, "ok  example.com/x\n[exit code 0]", false),
		}, VerdictVerified},

		{"only the README was edited, and a build ran", []session.Event{
			result(1, "edit_file", `{"path":"README.md"}`, "ok", false),
			result(1, "bash", `{"command":"go build ./..."}`, "\n[exit code 0]", false),
		}, VerdictNoChanges},
		{"only docs were edited, nothing ran", []session.Event{
			result(1, "write_file", `{"path":"docs/usage.rst"}`, "ok", false),
			result(1, "edit_file", `{"path":"LICENSE"}`, "ok", false),
		}, VerdictNoChanges},
		{"a doc and code were edited, nothing ran", []session.Event{
			result(1, "edit_file", `{"path":"README.md"}`, "ok", false),
			result(1, "edit_file", edit, "ok", false),
		}, VerdictUnverified},
		{"requirements.txt is not documentation", []session.Event{
			result(1, "edit_file", `{"path":"requirements.txt"}`, "ok", false),
		}, VerdictUnverified},
		{"check ran but nothing was edited", []session.Event{
			result(1, "bash", test, "ok\n[exit code 0]", false),
		}, VerdictNoChanges},
	}
	for _, c := range cases {
		got := FoldOutcome(c.events, 1)
		if got.Verdict != c.want {
			t.Fatalf("%s: verdict %q, want %q (%s)", c.name, got.Verdict, c.want, got.Why)
		}
		if got.Why == "" {
			t.Fatalf("%s: no reason given", c.name)
		}
	}
}

func TestGamingNotesDoNotChangeTheVerdict(t *testing.T) {
	ev := []session.Event{
		result(1, "write_file", `{"path":"mod.py","content":"def __eq__(self, other):\n    return True\n"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, "ok  \texample.com/x\t0.02s\n[exit code 0]", false),
	}
	got := FoldOutcome(ev, 1)
	if got.Verdict != VerdictVerified {
		t.Fatalf("verdict %q, want verified (gaming must not gate)", got.Verdict)
	}
	if len(got.Gaming) == 0 || !strings.Contains(got.Line(), "always-true-equality") {
		t.Fatalf("gaming missing: %+v line=%q", got.Gaming, got.Line())
	}
}

// A build passing is not a test passing. Compiling proves the change is
// valid, not that it works, and the reason has to say so.
func TestBuildAloneIsNotADemonstration(t *testing.T) {
	ev := []session.Event{
		result(0, "bash", `{"command":"go test ./parser"}`, "FAIL\n[exit code 1]", true),
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"go build ./..."}`, "\n[exit code 0]", false),
	}
	got := FoldOutcome(ev, 1)
	if got.Verdict != VerdictNotDemonstrated || got.Tested {
		t.Fatalf("verdict = %q tested=%v (%s)", got.Verdict, got.Tested, got.Why)
	}
	if !strings.Contains(got.Why, "compiles") {
		t.Fatalf("the reason should say what is missing: %q", got.Why)
	}
}

// A red-to-green flip is the strongest evidence and is named as such,
// but only when it is the same command that flipped.
func TestFlipIsNamedWhenItHappens(t *testing.T) {
	pass := "ok  \texample.com/x\t0.02s\n[exit code 0]"
	flipped := FoldOutcome([]session.Event{
		result(0, "bash", `{"command":"go test ./..."}`, "FAIL\n[exit code 1]", true),
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, pass, false),
	}, 1)
	if flipped.Verdict != VerdictVerified || !flipped.Flipped || !strings.Contains(flipped.Why, "had been failing") {
		t.Fatalf("flip = %+v", flipped)
	}
	// Passing without ever having been red is still verified, since the
	// tests ran — just without the stronger wording.
	fresh := FoldOutcome([]session.Event{
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, pass, false),
	}, 1)
	if fresh.Verdict != VerdictVerified || fresh.Flipped || strings.Contains(fresh.Why, "had been failing") {
		t.Fatalf("fresh = %+v", fresh)
	}
}

// The runners agree on very little, so what counts as "tests ran" is
// checked against what each of them actually prints.
func TestExercisedReadsRealRunnerOutput(t *testing.T) {
	for out, wantRan := range map[string]bool{
		"ok  \texample.com/x\t0.02s":                           true,
		"?   \texample.com/y\t[no test files]":                 false,
		"ok  \texample.com/x\t0.02s\n?\tex/y\t[no test files]": true,
		"===== 12 passed in 0.11s =====":                       true,
		"collected 0 items\n\nno tests ran in 0.01s":           false,
		"OK (7 tests)":                           true,
		"Ran 3 tests in 0.001s\n\nOK":            true,
		"test result: ok. 5 passed; 0 failed":    true,
		"Tests:       4 passed, 4 total":         true,
		"":                                       false,
		"Compiling foo v0.1.0\n    Finished dev": false,
		// A go test binary run directly prints PASS and no "ok" line.
		"=== RUN   TestX\n--- PASS: TestX (0.00s)\nPASS": true,
		// But a build log saying its own checks pass is not evidence.
		"all lint rules pass":           false,
		"PASSWORD=xxx\nbuild succeeded": false,
		// node --test, spec and TAP reporters (captured from node 22+).
		"ℹ tests 28\nℹ suites 7\nℹ pass 28\nℹ fail 0": true,
		"# tests 28\n# suites 7\n# pass 28\n# fail 0": true,
		"ℹ tests 0\nℹ suites 0\nℹ pass 0\nℹ fail 0":   false,
		// zig test, ctest, bun test (captured), mocha, bazel, dart.
		"1/2 t.test.a...OK\n2/2 t.test.b...OK\nAll 2 tests passed.": true,
		"100% tests passed, 0 tests failed out of 2":                true,
		" 1 pass\n 0 fail\nRan 1 test across 1 file. [104.00ms]":    true,
		"  5 passing (12ms)":                       true,
		"  0 passing (1ms)":                        false,
		"Executed 3 out of 3 tests: 3 tests pass.": true,
		"00:01 +5: All tests passed!":              true,
	} {
		ran, _ := exercised(out)
		if ran != wantRan {
			t.Fatalf("exercised(%q) ran=%v, want %v", out, ran, wantRan)
		}
	}
	// "found nothing" is only reported when nothing ran at all.
	if _, nothing := exercised("ok  \tx\t0.1s\n?\ty\t[no test files]"); nothing {
		t.Fatal("a partly-tested run was called empty")
	}
	for _, out := range []string{"ℹ tests 0\nℹ pass 0", "# tests 0\n# pass 0", "  0 passing (1ms)", "Test project /x/b\nNo tests were found!!!"} {
		if _, nothing := exercised(out); !nothing {
			t.Fatalf("an empty run was not noticed: %q", out)
		}
	}
	if _, nothing := exercised("collected 0 items"); !nothing {
		t.Fatal("a run that collected nothing was not noticed")
	}
}

// Distinct files are counted once each, however many times they are written.
func TestEditedCountsFilesNotCalls(t *testing.T) {
	ev := []session.Event{
		result(1, "edit_file", `{"path":"a.go","old_str":"1"}`, "ok", false),
		result(1, "edit_file", `{"path":"a.go","old_str":"2"}`, "ok", false),
		result(1, "write_file", `{"path":"b.go"}`, "ok", false),
		result(1, "edit_file", `{"path":"c.go"}`, "failed", true),
	}
	got := FoldOutcome(ev, 1)
	if got.Edited != 2 {
		t.Fatalf("edited = %d, want the two files that were written", got.Edited)
	}
}

func TestOverclaimAndCorrection(t *testing.T) {
	edited := []session.Event{result(1, "edit_file", `{"path":"a.go"}`, "ok", false)}
	// A claim the turn does not support.
	o := FoldOutcome(append(edited, say(1, "Fixed it — all tests pass now.")), 1)
	if o.Verdict != VerdictUnverified || !o.Overclaimed() {
		t.Fatalf("outcome = %+v", o)
	}
	if c := o.Correction(); !strings.Contains(c, "all tests pass") || !strings.Contains(c, "Run the project's tests") {
		t.Fatalf("correction = %q", c)
	}
	// The same state without the claim is reported, not corrected.
	quiet := FoldOutcome(append(edited, say(1, "I changed the parser.")), 1)
	if quiet.Overclaimed() {
		t.Fatalf("a plain answer was treated as a claim: %+v", quiet)
	}
	// A claim that the turn does support is not corrected either.
	ok := FoldOutcome([]session.Event{
		result(0, "bash", `{"command":"go test ./..."}`, "FAIL\n[exit code 1]", true),
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, "ok\n[exit code 0]", false),
		say(1, "The tests pass now."),
	}, 1)
	if ok.Verdict != VerdictVerified || ok.Overclaimed() {
		t.Fatalf("a supported claim was corrected: %+v", ok)
	}
}

// The loop puts an unsupported claim back to the model once, and the
// verdict lands on the log either way.
func TestTurnCorrectsAnOverclaimOnce(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.go","content":"package a\n"}`),
		text("Done — all tests pass."),
		text("You are right; I have not run them."),
	}}
	mem := &session.Memory{}
	a := &Agent{Provider: f, Model: "m", Log: mem, AutoApprove: true,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root})}
	var notices []string
	if err := a.Run(context.Background(), "add a.go", func(e Event) {
		if e.Kind == EvNotice || e.Kind == EvVerdict {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("%d model calls; the claim should have been put back once", len(f.requests))
	}
	joined := strings.Join(notices, " | ")
	if !strings.Contains(joined, "claims more than this turn shows") {
		t.Fatalf("no correction notice: %s", joined)
	}
	if !strings.Contains(joined, "unverified") {
		t.Fatalf("no verdict reported: %s", joined)
	}
	// The verdict is on the log, so it can be read back.
	var found bool
	for _, e := range mem.Events() {
		if e.Type == session.TypeVerdict {
			found = true
			if e.Name != string(VerdictUnverified) || !e.IsError {
				t.Fatalf("logged verdict = %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("the verdict was not logged")
	}
	// Switched off, nothing is corrected.
	g := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"b.go","content":"package b\n"}`),
		text("Done — all tests pass."),
	}}
	b := &Agent{Provider: g, Model: "m", Log: &session.Memory{}, AutoApprove: true,
		Off: map[string]bool{"verdict": true}, Tools: tool.NewRegistry(tool.WriteFile{Root: root})}
	if err := b.Run(context.Background(), "add b.go", nil); err != nil {
		t.Fatal(err)
	}
	if len(g.requests) != 2 {
		t.Fatalf("%d model calls with the verdict off", len(g.requests))
	}
}

// Both of these were wrong on real recorded sessions, and each produced a
// false accusation: the harness told the model it had not verified
// something when it had. A detector that misses a check is worse than one
// that counts a stray command, so both shapes are pinned here.
func TestCheckDetectionOnRealCommands(t *testing.T) {
	for cmd, want := range map[string]bool{
		"python test_ini.py":          true, // "_" is a word char, so \btest\b missed this
		"python -m pytest tests/ -v":  true,
		"go test ./...":               true,
		"npm run test:unit":           true,
		"cargo clippy":                true,
		"make check":                  true,
		"python -m unittest discover": true,
		"./run_tests.sh":              true,
		"ls -la":                      false,
		"cat access.log":              false,
		"git status":                  false,
	} {
		if got := shell.IsCheck(cmd); got != want {
			t.Fatalf("shell.IsCheck(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// Running the script that was just written is evidence the code executes.
// It is not a test, and the verdict has to say which of the two it is
// rather than claiming nothing ran.
func TestRunningTheEditedCodeIsNotNothing(t *testing.T) {
	ev := []session.Event{
		result(1, "write_file", `{"path":"summarize.sh"}`, "ok", false),
		result(1, "bash", `{"command":"chmod +x summarize.sh && ./summarize.sh access.log"}`, "10 1.2.3.4\n[exit code 0]", false),
	}
	got := FoldOutcome(ev, 1)
	if got.Verdict != VerdictNotDemonstrated {
		t.Fatalf("verdict = %q (%s)", got.Verdict, got.Why)
	}
	if !got.Executed || got.Tested {
		t.Fatalf("executed=%v tested=%v", got.Executed, got.Tested)
	}
	if !strings.Contains(got.Why, "nothing asserted") {
		t.Fatalf("the reason should say what is missing: %q", got.Why)
	}
	// Editing without running anything is still unverified.
	quiet := FoldOutcome([]session.Event{result(1, "write_file", `{"path":"summarize.sh"}`, "ok", false)}, 1)
	if quiet.Verdict != VerdictUnverified {
		t.Fatalf("verdict = %q", quiet.Verdict)
	}
	// A command that names an unrelated file is not evidence either.
	other := FoldOutcome([]session.Event{
		result(1, "write_file", `{"path":"summarize.sh"}`, "ok", false),
		result(1, "bash", `{"command":"cat access.log"}`, "…", false),
	}, 1)
	if other.Verdict != VerdictUnverified || other.Executed {
		t.Fatalf("unrelated command counted: %+v", other)
	}
}

// A green produced by a suite the same turn edited is not the same as a
// green against tests that were already there. The harness cannot tell an
// honest regression test from marking your own homework, so it names the
// difference and leaves the judgement to the reader.
func TestTestsEditedInTheSameTurn(t *testing.T) {
	pass := "ok  \texample.com/x\t0.02s\n[exit code 0]"
	got := FoldOutcome([]session.Event{
		result(1, "edit_file", `{"path":"parse.go"}`, "ok", false),
		result(1, "edit_file", `{"path":"parse_test.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, pass, false),
	}, 1)
	if got.Verdict != VerdictSelfChecked {
		t.Fatalf("verdict = %q (%s)", got.Verdict, got.Why)
	}
	if len(got.TestsEdited) != 1 || got.TestsEdited[0] != "parse_test.go" {
		t.Fatalf("tests edited = %v", got.TestsEdited)
	}
	if !strings.Contains(got.Why, "changed in the same turn") {
		t.Fatalf("the reason should name the caveat: %q", got.Why)
	}
	// The same turn without touching the tests is plainly verified.
	clean := FoldOutcome([]session.Event{
		result(1, "edit_file", `{"path":"parse.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, pass, false),
	}, 1)
	if clean.Verdict != VerdictVerified || len(clean.TestsEdited) != 0 {
		t.Fatalf("clean turn = %+v", clean)
	}
}

// A locked accept-test file (see accept_lock.go) cannot be edited by any tool
// call while it is locked: the attempt is refused and logged as an
// error result, never as a successful write. FoldOutcome only counts
// successful edits toward TestsEdited, so a locked test's denied edit
// must not turn a real green into a self-checked one — the model could
// not have touched it either way.
func TestVerdictNotSelfCheckedWhenTheTestEditWasDenied(t *testing.T) {
	pass := "ok  \texample.com/x\t0.02s\n[exit code 0]"
	got := FoldOutcome([]session.Event{
		result(1, "edit_file", `{"path":"parse_test.go"}`, "parse_test.go is a locked acceptance test for \"parse ints\": it may not be changed by any tool call.", true),
		result(1, "edit_file", `{"path":"parse.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, pass, false),
	}, 1)
	if got.Verdict != VerdictVerified {
		t.Fatalf("verdict = %q (%s), want verified: a denied edit of a locked test is not a self-check", got.Verdict, got.Why)
	}
	if len(got.TestsEdited) != 0 {
		t.Fatalf("tests edited = %v, want none: the edit was refused, not applied", got.TestsEdited)
	}
}

func TestTestFileDetection(t *testing.T) {
	for path, want := range map[string]bool{
		"parse_test.go":               true,
		"internal/agent/todo_test.go": true,
		"tests/test_ledger.py":        true,
		"test_ini.py":                 true,
		"src/foo.spec.ts":             true,
		"src/foo.test.tsx":            true,
		"spec/models/user_spec.rb":    true,
		"conftest.py":                 true,
		"parse.go":                    false,
		"internal/agent/todo.go":      false,
		"latest/contest.go":           false,
		"README.md":                   false,
	} {
		if got := isTestFile(path); got != want {
			t.Fatalf("isTestFile(%q) = %v, want %v", path, got, want)
		}
	}
}

// Writing code with its tests and running them is the normal shape of the
// job, so it must not be reported as a failure — a third of real turns
// look like this, and an alarm that often is no alarm at all.
func TestSelfCheckedIsACaveatNotAnAlarm(t *testing.T) {
	o := FoldOutcome([]session.Event{
		result(1, "write_file", `{"path":"ledger.py"}`, "ok", false),
		result(1, "write_file", `{"path":"tests/test_ledger.py"}`, "ok", false),
		result(1, "bash", `{"command":"pytest tests/"}`, "12 passed in 0.11s\n[exit code 0]", false),
		say(1, "The tests pass."),
	}, 1)
	if o.Verdict != VerdictSelfChecked {
		t.Fatalf("verdict = %q", o.Verdict)
	}
	if !o.OK() {
		t.Fatal("a turn that wrote code, wrote tests and ran them is not a failure")
	}
	if o.Overclaimed() {
		t.Fatal("saying the tests pass, when they did, is not an overclaim")
	}
	// The caveat is still visible in the line the user reads.
	if !strings.Contains(o.Line(), "self-checked") || !strings.Contains(o.Line(), "same turn") {
		t.Fatalf("line = %q", o.Line())
	}
}

// TestInlineAssertionsAreSelfChecked: a model that writes ini.py and then
// runs `python3 -c "... assert ..."` scripts against the spec, all
// passing, has checked its work — with checks it wrote. That is the
// self-checked verdict, not "nothing asserted the result", which is what
// it was called before and what sent it back to work for no reason.
func TestInlineAssertionsAreSelfChecked(t *testing.T) {
	o := FoldOutcome([]session.Event{
		result(1, "write_file", `{"path":"ini.py"}`, "ok", false),
		result(1, "bash", `{"command":"python3 -c 'from ini import parse; assert parse(\"[a]\\nx=1\") == {\"a\": {\"x\": 1}}; print(\"All tests passed!\")'"}`, "All tests passed!\n[exit code 0]", false),
		say(1, "Done."),
	}, 1)
	if o.Verdict != VerdictSelfChecked {
		t.Fatalf("verdict = %q (%s)", o.Verdict, o.Why)
	}
	if !o.OK() || o.Overclaimed() {
		t.Fatalf("passing self-written assertions is not a failure or an overclaim: %+v", o)
	}
	// Running the file with no assertion in it is still only "executed".
	o = FoldOutcome([]session.Event{
		result(1, "write_file", `{"path":"ini.py"}`, "ok", false),
		result(1, "bash", `{"command":"python3 ini.py"}`, "\n[exit code 0]", false),
	}, 1)
	if o.Verdict != VerdictNotDemonstrated {
		t.Fatalf("verdict = %q", o.Verdict)
	}
}

// A call the harness refused never ran, so it is not a check that failed.
// A dogfooding session had turns 1-4 all read "check failed" because an
// accept-mode refusal of `find`, a denied heredoc and a cancelled prompt
// were each counted as a red check. Old logs carry no Denied flag, so the
// refusal text itself is recognised too.
func TestRefusedCallsAreNotFailedChecks(t *testing.T) {
	pass := "ok  \texample.com/x\t0.02s\n[exit code 0]"
	refused := func(args, out string, flag bool) session.Event {
		e := result(1, "bash", args, out, true)
		e.Denied = flag
		return e
	}
	got := FoldOutcome([]session.Event{
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		refused(`{"command":"go test ./internal/x"}`, "this session is writing acceptance tests", true),
		refused(`{"command":"cat > /tmp/trace_test.go <<'EOF'\npackage x\nEOF"}`, DeniedMessage, false),
		refused(`{"command":"go vet ./..."}`, DeniedMessage+" The user said: "+CancelledReason, false),
		result(1, "bash", `{"command":"go test ./..."}`, pass, false),
	}, 1)
	if got.Verdict != VerdictVerified || got.Failed != 0 || got.Ran != 1 {
		t.Fatalf("outcome = %+v, want verified with the one real run counted", got)
	}
}

// A gate the harness ran at the end of the turn is a check the turn ran:
// green after an earlier red run of the same command, the turn is not
// "check failed", and a claim that the tests pass is not an overclaim.
// The model's `cd <root> &&` prefix does not make it a different command.
func TestGateRunSupersedesEarlierRedRunOfTheSameCommand(t *testing.T) {
	root := "/work/repo"
	gate := session.Event{Type: session.TypeGate, Turn: 1, Name: "go test ./internal/x", Source: session.GateAccept}
	events := []session.Event{
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"cd /work/repo && go test ./internal/x"}`, "FAIL\n[exit code 1]", true),
		say(1, "Done — the tests pass now."),
		gate,
	}
	got := FoldOutcomeIn(events, 1, root)
	if got.Verdict != VerdictVerified || got.Failed != 0 || got.Overclaimed() {
		t.Fatalf("outcome = %+v, want verified: the latest run of the command was green", got)
	}
	if len(got.Checks) != 1 || got.Checks[0] != "go test ./internal/x" {
		t.Fatalf("checks = %q, want the command without the cd prefix, once", got.Checks)
	}
	// Still red at the end: the latest result is what counts.
	red := append(append([]session.Event(nil), events[:2]...),
		result(1, "bash", `{"command":"go test ./internal/x"}`, "FAIL\n[exit code 1]", true))
	if o := FoldOutcomeIn(red, 1, root); o.Verdict != VerdictCheckFailed {
		t.Fatalf("outcome = %+v, want check failed", o)
	}
}

// A check's command is kept whole enough to read: the dogfooding proof
// showed `cd /tmp/claude-1000/...-4e0c-…`, which hid the command itself.
func TestCheckCommandsKeepTheCommand(t *testing.T) {
	long := "go test ./internal/agent -run 'TestRefusedCallsAreNotFailedChecks|TestGateRunSupersedesEarlierRedRunOfTheSameCommand|TestCheckCommandsKeepTheCommand' -count=1"
	got := FoldOutcomeIn([]session.Event{
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"cd /tmp/claude-1000/-home-driver-wspace-janetcode/9d6e14c9-d4e6-4e0c-a3e5-cd6a99729461 && `+long+`"}`, "ok  \tx\t0.1s", false),
	}, 1, "/tmp/claude-1000/-home-driver-wspace-janetcode/9d6e14c9-d4e6-4e0c-a3e5-cd6a99729461")
	if len(got.Checks) != 1 || got.Checks[0] != long {
		t.Fatalf("checks = %q, want %q", got.Checks, long)
	}
}

// In the /accept writing phase the new tests are supposed to fail: a turn
// that wrote only tests and saw them red has done exactly its job.
func TestAcceptWritingTurnWithRedTestsIsTestsWritten(t *testing.T) {
	start := session.Event{Type: session.TypeTurnStart, Turn: 1, Reason: session.TurnStartAccept + string(session.AcceptWriting)}
	got := FoldOutcome([]session.Event{
		start,
		result(1, "write_file", `{"path":"clip_test.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, "--- FAIL: TestClip\nFAIL\n[exit code 1]", true),
	}, 1)
	if got.Verdict != VerdictTestsWritten || !got.OK() || !strings.Contains(got.Why, "failing as expected") {
		t.Fatalf("outcome = %+v, want tests written (failing as expected)", got)
	}
	// An implementation file edited in the same turn is not the writing
	// phase's expected state, and neither is a turn outside accept mode.
	for name, ev := range map[string][]session.Event{
		"implementation edited": {start, result(1, "write_file", `{"path":"clip.go"}`, "ok", false), result(1, "bash", `{"command":"go test ./..."}`, "FAIL", true)},
		"not in accept mode":    {result(1, "write_file", `{"path":"clip_test.go"}`, "ok", false), result(1, "bash", `{"command":"go test ./..."}`, "FAIL", true)},
	} {
		if o := FoldOutcome(ev, 1); o.Verdict != VerdictCheckFailed {
			t.Fatalf("%s: verdict = %q, want check failed", name, o.Verdict)
		}
	}
}

// The bash tool does not mark a non-zero exit as an error; the
// "[exit code N]" line it appends is what the model reads, and so must
// the verdict — otherwise a red test run is counted as a pass.
func TestExitCodeLineMakesACheckRed(t *testing.T) {
	got := FoldOutcome([]session.Event{
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, "--- FAIL: TestX\nFAIL\n[exit code 1]", false),
	}, 1)
	if got.Verdict != VerdictCheckFailed {
		t.Fatalf("verdict = %q, want check failed", got.Verdict)
	}
	// grep's exit 1 is an answer, not a failure, and the tool says so.
	got = FoldOutcome([]session.Event{
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"grep -rn TODO ./internal/check"}`, "(no output)\n[exit code 1] nothing matched; for this command that is an answer, not a failure", false),
	}, 1)
	if got.Failed != 0 {
		t.Fatalf("grep finding nothing counted as a failed check: %+v", got)
	}
}

// A recorded session wrote a tcp proxy, ran `python -m pytest` on a box
// whose binary is python3, fixed the command, and saw four tests pass.
// The turn was reported as "check failed": the dead command kept its own
// entry in the check table forever, because the retry was not written
// character for character the same way.
func TestATypoedInterpreterIsNotARedCheck(t *testing.T) {
	ev := []session.Event{
		result(1, "write_file", `{"path":"tcp_proxy.py"}`, "Created tcp_proxy.py", false),
		result(1, "write_file", `{"path":"test_tcp_proxy.py"}`, "Created test_tcp_proxy.py", false),
		result(1, "bash", `{"command":"python -m pytest test_tcp_proxy.py -v"}`,
			"/usr/bin/bash: line 1: python: command not found\n\n[exit code 127]", false),
		result(1, "bash", `{"command":"python3 -m pytest test_tcp_proxy.py -v"}`,
			"============================== 4 passed in 0.10s ===============================\n", false),
	}
	got := FoldOutcomeIn(ev, 1, "")
	if got.Failed != 0 || got.Ran != 1 {
		t.Fatalf("ran %d passed %d failed %d, checks %v", got.Ran, got.Passed, got.Failed, got.Checks)
	}
	if !got.Tested || !got.OK() {
		t.Fatalf("verdict = %q (%s), tested=%v", got.Verdict, got.Why, got.Tested)
	}
}

// The same session's sibling: `pip install -e . && python -m mutest
// tests/` died on an externally-managed environment, and the model ran
// the suite from a venv instead. One check, ending green.
func TestACheckReRunAnotherWayRetiresTheRedOne(t *testing.T) {
	ev := []session.Event{
		result(1, "write_file", `{"path":"mutest/core.py"}`, "Created core.py", false),
		result(1, "bash", `{"command":"pip install -e . -q && python -m mutest tests/"}`,
			"error: externally-managed-environment\n\n[exit code 1]", false),
		result(1, "bash", `{"command":".venv/bin/mutest tests/"}`, "12 passed\n\n[exit code 0]", false),
	}
	got := FoldOutcomeIn(ev, 1, "")
	if got.Failed != 0 || got.Ran != 1 {
		t.Fatalf("ran %d passed %d failed %d, checks %v", got.Ran, got.Passed, got.Failed, got.Checks)
	}
	if !got.OK() {
		t.Fatalf("verdict = %q (%s)", got.Verdict, got.Why)
	}
}

// A scratch script that came back red and was rewritten into one that
// came back green is the model fixing its own check, not a red check.
func TestARewrittenScratchCheckRetiresTheRedOne(t *testing.T) {
	ev := []session.Event{
		result(1, "edit_file", `{"path":"reports/paging.py"}`, "Edited reports/paging.py", false),
		result(1, "bash", `{"command":"python -m pytest -q && python3 - <<'EOF'\nassert page(1) == 2\nEOF"}`, "4 passed\nAssertionError\n[exit code 1]", false),
		result(1, "bash", `{"command":"python3 - <<'EOF'\nassert page(1) == 1\nprint('ok')\nEOF\npython -m pytest -q"}`, "ok\n4 passed\n[exit code 0]", false),
	}
	got := FoldOutcomeIn(ev, 1, "")
	if got.Failed != 0 || got.Verdict == VerdictCheckFailed {
		t.Fatalf("verdict = %q, failed %d, checks %v", got.Verdict, got.Failed, got.Checks)
	}
}

// A script the turn wrote for itself, or one outside the project, is a
// scratch check too: a red run of it is retired by a later green scratch
// check, as the model rewrote its own expectations.
func TestARedScratchFileIsRetiredByALaterGreenOne(t *testing.T) {
	ev := []session.Event{
		result(1, "edit_file", `{"path":"pkg/exec.py"}`, "Edited pkg/exec.py", false),
		result(1, "bash", `{"command":"python /tmp/check_incremental.py"}`, "ModuleNotFoundError\n[exit code 1]", false),
		result(1, "write_file", `{"path":"scratch_check.py"}`, "Created scratch_check.py (300 lines)", false),
		result(1, "bash", `{"command":"python scratch_check.py && python -m pytest -q"}`, "ALL PASSED\n6 passed\n[exit code 0]", false),
	}
	got := FoldOutcomeIn(ev, 1, "/ws")
	if got.Failed != 0 || got.Verdict == VerdictCheckFailed {
		t.Fatalf("verdict = %q, failed %d, checks %v", got.Verdict, got.Failed, got.Checks)
	}
}

// A build failure is not retired by a test run over the same target: the
// two ask different questions, and the red one still has its answer.
func TestADifferentKindOfCheckDoesNotRetireARedOne(t *testing.T) {
	ev := []session.Event{
		result(1, "write_file", `{"path":"a.go"}`, "Created a.go", false),
		result(1, "bash", `{"command":"go build ./..."}`, "a.go:3: undefined: x\n[exit code 1]", false),
		result(1, "bash", `{"command":"go test ./..."}`, "ok  pkg\n[exit code 0]", false),
	}
	got := FoldOutcomeIn(ev, 1, "")
	if got.Failed != 1 || got.Verdict != VerdictCheckFailed {
		t.Fatalf("verdict = %q, failed %d, checks %v", got.Verdict, got.Failed, got.Checks)
	}
}

// A test runner that comes back red has still run tests. The recorded
// mutest session — a testing library whose example suite deliberately
// contains one failing test to demonstrate failure reporting — was
// folded as Tested=false on every turn.
func TestARedSuiteStillCountsAsTested(t *testing.T) {
	ev := []session.Event{
		result(1, "write_file", `{"path":"mutest/core.py"}`, "Created core.py", false),
		result(1, "bash", `{"command":".venv/bin/mutest tests/"}`,
			"11 passed, 1 failed\n[exit code 1]", false),
	}
	got := FoldOutcomeIn(ev, 1, "")
	if !got.Tested {
		t.Fatalf("tested=false after a suite ran red: %q (%s)", got.Verdict, got.Why)
	}
	if got.Verdict != VerdictCheckFailed {
		t.Fatalf("verdict = %q, want the red check to still fail the turn", got.Verdict)
	}
}

// The recorded mutest session wrote an example suite, ran it, and found
// one of its own assertions wrong: int("x") raises ValueError, not
// TypeError. Correcting it was reported as gaming — "an existing test's
// expectation was changed" — for a test four minutes old.
func TestFixingATestWrittenThisTurnIsNotGaming(t *testing.T) {
	ev := []session.Event{
		result(1, "write_file", `{"path":"tests/test_examples.py","content":"assert_raises(TypeError, int, \"x\")"}`,
			"Created tests/test_examples.py (104 lines)", false),
		result(1, "edit_file", `{"path":"tests/test_examples.py","old_string":"assert_raises(TypeError, int, \"x\")","new_string":"assert_raises(ValueError, int, \"x\")"}`,
			"Edited tests/test_examples.py", false),
		result(1, "bash", `{"command":".venv/bin/mutest tests/"}`, "12 passed\n[exit code 0]", false),
	}
	if got := FoldOutcomeIn(ev, 1, ""); len(got.Gaming) != 0 {
		t.Fatalf("gaming = %v", got.Gaming)
	}
	// An expectation in a file that predates the turn is still reported.
	ev[0] = result(1, "bash", `{"command":"ls"}`, "", false)
	if got := FoldOutcomeIn(ev, 1, ""); len(got.Gaming) != 1 {
		t.Fatalf("gaming = %v, want the untouched detector to still fire", got.Gaming)
	}
}

// A recorded turn ran five different `python3 -c` scripts and the
// outcome recorded each of them as `python3 -c "` — everything before the
// first newline — so the proof said five checks ran and nothing about
// what any of them was.
func TestAMultiLineCheckIsRecordedWhole(t *testing.T) {
	script := "python3 -c \"\nimport mutest\nimport test_examples\ncases = mutest._discover_test_cases(test_examples)\nprint(cases)\""
	got := checkLabel(script)
	if strings.Contains(got, "\n") {
		t.Fatalf("label spans lines: %q", got)
	}
	for _, want := range []string{"import mutest", "_discover_test_cases"} {
		if !strings.Contains(got, want) {
			t.Fatalf("label %q lost %q", got, want)
		}
	}
	// Two scripts that differ only past the first line are told apart.
	other := strings.Replace(script, "print(cases)", "print(len(cases))", 1)
	if checkLabel(other) == got {
		t.Fatalf("two different scripts share the label %q", got)
	}
	// A single-line command is untouched.
	if got := checkLabel("go test ./..."); got != "go test ./..." {
		t.Fatalf("single line = %q", got)
	}
}

// Adding tests is the healthy workflow — a new project's first suite, or
// "add the feature and a test for it" — and must not be flagged the way
// changing an existing test is. Only an edit that rewrites or removes
// lines already in a test file makes the green self-checked.
func TestAddingTestsIsNotSelfChecked(t *testing.T) {
	pass := result(1, "bash", `{"command":"cargo test"}`, "test result: ok. 3 passed; 0 failed\n[exit code 0]", false)
	cases := []struct {
		name   string
		events []session.Event
		want   Verdict
	}{
		{"a test file created this turn", []session.Event{
			result(1, "write_file", `{"path":"src/lib.rs","content":"pub fn f() {}"}`, "Created src/lib.rs (1 lines)", false),
			result(1, "write_file", `{"path":"tests/it_test.rs","content":"#[test] fn t() {}"}`, "Created tests/it_test.rs (1 lines)", false),
			pass,
		}, VerdictVerified},
		{"a test appended to an existing file", []session.Event{
			result(1, "edit_file", `{"path":"src/lib.rs","old_string":"a","new_string":"b"}`, "ok", false),
			result(1, "edit_file", `{"path":"tests/it_test.rs","old_string":"fn t() {}","new_string":"fn t() {}\n#[test] fn json() {}"}`, "ok", false),
			pass,
		}, VerdictVerified},
		{"an existing assertion rewritten", []session.Event{
			result(1, "edit_file", `{"path":"src/lib.rs","old_string":"a","new_string":"b"}`, "ok", false),
			result(1, "edit_file", `{"path":"tests/it_test.rs","old_string":"assert_eq!(f(), 2)","new_string":"assert_eq!(f(), 3)"}`, "ok", false),
			pass,
		}, VerdictSelfChecked},
		{"a test file overwritten wholesale", []session.Event{
			result(1, "edit_file", `{"path":"src/lib.rs","old_string":"a","new_string":"b"}`, "ok", false),
			result(1, "write_file", `{"path":"tests/it_test.rs","content":"#[test] fn t() {}"}`, "Wrote tests/it_test.rs (1 lines)", false),
			pass,
		}, VerdictSelfChecked},
		{"a test replaced by line range", []session.Event{
			result(1, "edit_file", `{"path":"src/lib.rs","old_string":"a","new_string":"b"}`, "ok", false),
			result(1, "edit_file", `{"path":"tests/it_test.rs","from_line":3,"to_line":4,"new_string":"x"}`, "ok", false),
			pass,
		}, VerdictSelfChecked},
	}
	for _, c := range cases {
		if got := FoldOutcome(c.events, 1); got.Verdict != c.want {
			t.Errorf("%s: verdict %q, want %q (%s)", c.name, got.Verdict, c.want, got.Why)
		}
	}
}

// "Tests are failing, fix it" is usually one turn: the model runs the
// suite, sees it red, fixes the code, runs it green. That is the
// strongest evidence there is and must be named as a flip, not only
// when the red run happened in an earlier turn.
func TestRedThenGreenInOneTurnIsAFlip(t *testing.T) {
	test := `{"command":"python -m pytest -q"}`
	o := FoldOutcome([]session.Event{
		result(1, "bash", test, "3 failed, 2 passed in 0.01s\n[exit code 1]", true),
		result(1, "edit_file", `{"path":"dates.py","old_string":"a","new_string":"b"}`, "ok", false),
		{Type: session.TypeToolResult, Turn: 1, CallID: "c2", Name: "bash", Arguments: test, Output: "5 passed in 0.01s\n[exit code 0]"},
	}, 1)
	if o.Verdict != VerdictVerified || !o.Flipped || !strings.Contains(o.Why, FlippedWhy) {
		t.Fatalf("verdict = %q flipped=%v (%s)", o.Verdict, o.Flipped, o.Why)
	}
	// Green, then edited, then green again flipped nothing.
	o = FoldOutcome([]session.Event{
		result(1, "bash", test, "5 passed\n[exit code 0]", false),
		result(1, "edit_file", `{"path":"dates.py","old_string":"a","new_string":"b"}`, "ok", false),
		{Type: session.TypeToolResult, Turn: 1, CallID: "c2", Name: "bash", Arguments: test, Output: "5 passed\n[exit code 0]"},
	}, 1)
	if o.Flipped {
		t.Fatalf("a suite that was never red flipped: %s", o.Why)
	}
	// Red only because of this turn's own edit — a new project that did
	// not compile at first — was not failing before, so it is no fix.
	o = FoldOutcome([]session.Event{
		result(1, "write_file", `{"path":"cache.go","content":"package c"}`, "Created cache.go (1 lines)", false),
		result(1, "bash", test, "FAILED\n[exit code 1]", true),
		result(1, "edit_file", `{"path":"cache.go","old_string":"a","new_string":"b"}`, "ok", false),
		{Type: session.TypeToolResult, Turn: 1, CallID: "c2", Name: "bash", Arguments: test, Output: "5 passed\n[exit code 0]"},
	}, 1)
	if o.Flipped {
		t.Fatalf("the turn's own breakage, repaired, counted as a fix: %s", o.Why)
	}
}

func TestVerdictWordsOneFileAndTextFiles(t *testing.T) {
	for path, doc := range map[string]bool{"hello.txt": true, "notes/todo.txt": true, "requirements.txt": false, "requirements-dev.txt": false, "main.go": false} {
		if isDocFile(path) != doc {
			t.Errorf("isDocFile(%q) = %v, want %v", path, !doc, doc)
		}
	}
}

func TestUnverifiedOneFileSaysIt(t *testing.T) {
	edit := `{"path":"a.go","old_str":"x","new_str":"y"}`
	got := FoldOutcome([]session.Event{result(1, "edit_file", edit, "ok", false)}, 1)
	if got.Why != "1 file edited and nothing was run against it" {
		t.Fatalf("why = %q", got.Why)
	}
	txt := FoldOutcome([]session.Event{result(1, "write_file", `{"path":"hello.txt","content":"hi"}`, "ok", false)}, 1)
	if txt.Verdict != VerdictNoChanges {
		t.Fatalf("a text file edit: %q (%s)", txt.Verdict, txt.Why)
	}
}

// A command that never started in an earlier turn — the binary was not
// on PATH — was remembered as a red check, so the same command passing
// in a later turn was reported as "tests that had been failing now
// pass": a fix claimed for code no test had ever failed on.
func TestAnEarlierCommandNotFoundIsNotAFailingCheck(t *testing.T) {
	test := `{"command":"pytest tests/"}`
	ev := []session.Event{
		result(1, "bash", test, "bash: line 1: pytest: command not found\n\n[exit code 127]", false),
		result(2, "edit_file", `{"path":"a.py"}`, "ok", false),
		result(2, "bash", test, "3 passed in 0.02s\n[exit code 0]", false),
	}
	got := FoldOutcomeIn(ev, 2, "")
	if got.Verdict != VerdictVerified {
		t.Fatalf("verdict = %q (%s)", got.Verdict, got.Why)
	}
	if got.Flipped || strings.Contains(got.Why, FlippedWhy) {
		t.Fatalf("a command that never ran counted as a red check now green: %+v", got)
	}
}

// A red run is retired by a later green run of the same check however its
// output was piped, and only by one that covered what the red one ran:
// the whole target, or the same selection.
func TestSupersededBySeesThroughPipes(t *testing.T) {
	cases := []struct {
		red, green string
		want       bool
	}{
		{"go test ./pkg/ 2>&1 | head -40", "go test ./pkg/ 2>&1 | tail -3", true},
		{"go test ./pkg/ -run TestFormatter 2>&1 | sed -n 1,80p", "go test ./pkg/ 2>&1 | tail -3", true},
		{"go test ./pkg/ -run TestA", "go test ./pkg/ -run TestA -v", true},
		{"go test ./pkg/ -run TestA", "go test ./pkg/ -run TestB", false},
		{"go test ./pkg/", "go test ./other/", false},
		{"go build ./...", "go test ./...", false},
	}
	for _, c := range cases {
		if got := supersededBy(c.red, c.green, nil); got != c.want {
			t.Errorf("supersededBy(%q, %q) = %v, want %v", c.red, c.green, got, c.want)
		}
	}
}
