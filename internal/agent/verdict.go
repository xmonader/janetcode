package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// What actually happened in a turn, as opposed to what the model says
// happened. A model that edits three files and reports "done, tests pass"
// is making a claim; the harness watched the turn and knows whether a test
// ran at all. This computes the verdict from the events, states it next to
// the model's answer, and — when the claim and the evidence disagree —
// tells the model so it cannot quietly leave the difference standing.
//
// Every verdict here reports. None of them blocks: a gate that refuses to
// let a turn finish wedges more work than it saves, and the user can see
// the verdict and decide.

// Verdict is the outcome of one turn's changes.
type Verdict string

const (
	// VerdictNoChanges: nothing was edited, so there is nothing to verify.
	VerdictNoChanges Verdict = "no changes"
	// VerdictVerified: something was edited and a check ran and passed
	// afterwards, having failed earlier in the session — the change is
	// demonstrated to have done something.
	VerdictVerified Verdict = "verified"
	// VerdictNotDemonstrated: a check passed, but it was passing before
	// the change too, so it does not show the change works.
	VerdictNotDemonstrated Verdict = "not demonstrated"
	// VerdictUnverified: code was edited and nothing was run.
	VerdictUnverified Verdict = "unverified"
	// VerdictCheckFailed: a check ran and came back red.
	VerdictCheckFailed Verdict = "check failed"
	// VerdictVacuous: a check passed without exercising anything — no
	// tests collected, no files matched. Green, and worth nothing.
	VerdictVacuous Verdict = "vacuous"
	// VerdictSelfChecked: tests passed, but the tests themselves were
	// edited in the same turn. Legitimate when a rename or a new
	// regression test required it, and the shape of marking your own
	// homework otherwise; the harness cannot tell which, so it says so.
	VerdictSelfChecked Verdict = "self-checked"
)

// TurnOutcome is the verdict with what it was decided from, so the user
// and the model can both see the reasoning rather than a bare word.
type TurnOutcome struct {
	Verdict  Verdict `json:"verdict"`
	Edited   int     `json:"edited"`   // files changed
	Ran      int     `json:"ran"`      // check commands run
	Passed   int     `json:"passed"`   // of those, that exited 0
	Failed   int     `json:"failed"`   // of those, that did not
	Flipped  bool    `json:"flipped"`  // a command that had failed now passes
	Tested   bool    `json:"tested"`   // a check reported tests actually running
	Executed bool    `json:"executed"` // a command ran a file edited this turn
	// TestsEdited are the test files changed in this turn, which weakens
	// a green produced by running them.
	TestsEdited []string `json:"tests_edited,omitempty"`
	// Checks are the commands counted as checks, newest last.
	Checks []string `json:"checks,omitempty"`
	// Why is the one-line reason for the verdict.
	Why string `json:"why"`
	// Claimed is what the model asserted about verification, when it
	// asserted anything a check could have shown.
	Claimed string `json:"claimed,omitempty"`
}

// OK reports whether the turn checked its work. A self-checked turn
// counts: writing code with the tests for it and running them is the
// normal shape of the job, and treating it as a failure would cry wolf on
// a third of all turns. The caveat lives in the verdict and its reason,
// not in an alarm. Only Verdict == VerdictVerified is the strong claim.
func (o TurnOutcome) OK() bool {
	switch o.Verdict {
	case VerdictVerified, VerdictSelfChecked, VerdictNoChanges:
		return true
	}
	return false
}

// Line renders the verdict for the user, in one line.
func (o TurnOutcome) Line() string {
	mark := "—"
	switch o.Verdict {
	case VerdictVerified:
		mark = "✓"
	case VerdictCheckFailed, VerdictVacuous:
		mark = "✗"
	case VerdictUnverified, VerdictNotDemonstrated, VerdictSelfChecked:
		mark = "⚠"
	}
	return fmt.Sprintf("%s %s: %s", mark, o.Verdict, o.Why)
}

// Reading a check's output for what it actually exercised. Exit 0 is not
// evidence on its own: a build proves the code compiles, a test run that
// collected nothing proves less than that, and only tests that ran and
// passed say the change behaves. These patterns are deliberately the ones
// the common runners print, and anything unrecognised counts as no
// evidence rather than as evidence.

// ranRe matches a runner reporting that tests actually executed. Every
// alternative is anchored or carries a count: "pass" on its own, loose in
// prose, is what a build log says about itself and is not evidence.
var ranRe = regexp.MustCompile(`(?im)(^ok\s+\S+|^PASS$|\d+ passed|\d+ tests?,|test result: ok\.|Tests:\s+\d+|OK \(\d+ test|Ran \d+ test|\d+ examples?, \d+ failures)`)

// nothingRe matches a runner saying it found nothing to run. A green that
// collected no tests is the easiest way for a change to look done when
// nothing checked it.
var nothingRe = regexp.MustCompile(`(?i)(no tests? (ran|were run|to run|found|collected|executed)|collected 0 items|ran 0 tests|0 passed|empty test suite|no test suites found)`)

// exercised reports what a passing check's output shows. ran is true when
// tests executed; nothing is true when the runner said it found none. A
// Go run that reports "ok" for one package and "[no test files]" for
// another has exercised something, so the two are not opposites.
func exercised(out string) (ran, nothing bool) {
	ran = ranRe.MatchString(out)
	nothing = !ran && nothingRe.MatchString(out)
	return ran, nothing
}

// checkRe recognises a command that checks something. It is looser than
// the trajectory features' pattern on purpose: that one matches whole
// words, so `python test_ini.py` — a real test run — slipped past it,
// because "_" is a word character and \btest\b never reached the "test"
// in "test_ini". A detector that misses a check accuses the model of not
// running one, which is worse than counting a stray command.
var checkRe = regexp.MustCompile(`(?i)(test|spec|pytest|jest|vitest|mocha|rspec|junit|check|verify|assert|vet\b|lint|tsc\b|mypy|ruff|clippy|build|make\b|ctest|tox|nox|gradle|mvn\b)`)

// isCheck reports whether a shell command is checking something.
func isCheck(cmd string) bool { return cmd != "" && checkRe.MatchString(cmd) }

// inlineAssertRe matches a command that carries its own assertions — a
// `python -c "... assert ..."` or a `node -e` written for the occasion.
// Passing, it is evidence of the same kind as a test file written in the
// same turn: the code meets checks the model wrote, not checks that were
// already there.
var inlineAssertRe = regexp.MustCompile(`(?i)\bassert\w*\b`)

// ranEdited reports whether a command executes a file edited in this
// turn — running the script you just wrote. It is evidence the code at
// least executes, which is not nothing and is not a test either.
func ranEdited(cmd string, edited map[string]bool) bool {
	if cmd == "" {
		return false
	}
	for path := range edited {
		base := path
		if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
			base = base[i+1:]
		}
		if base == "" || len(base) < 3 {
			continue
		}
		if strings.Contains(cmd, base) {
			return true
		}
	}
	return false
}

// testFileRe matches a path that holds tests. A green produced by a suite
// that was itself edited in the same turn is weaker evidence than one
// against tests that were already there: at the honest end the model
// wrote a regression test with the fix, and at the other it changed the
// thing that was failing until it stopped failing. The harness cannot
// tell those apart, so it says which kind of green this is and leaves the
// judgement to the reader.
var testFileRe = regexp.MustCompile(`(?i)((^|/)tests?/|(^|/)spec/|_test\.[a-z]+$|_spec\.[a-z]+$|(^|/)test_[^/]+$|\.test\.[a-z]+$|\.spec\.[a-z]+$|(^|/)conftest\.py$|Test[A-Z][^/]*\.java$)`)

// isTestFile reports whether a path holds tests.
func isTestFile(path string) bool { return path != "" && testFileRe.MatchString(path) }

// claimRe matches an assertion a check could have demonstrated. It is
// deliberately narrow: a claim about tests, builds or verification, not
// every use of the word "done".
var claimRe = regexp.MustCompile(`(?i)\b(tests? (now )?(pass|passes|passing|are green|succeed)|all tests? pass|build (succeeds|passes|is green|works)|verified|I (ran|have run) the tests?|checks? pass)\b`)

// FoldOutcome decides the verdict for one turn from its events. It reads
// only what the harness observed: which files were written, which
// commands ran, and how they exited.
func FoldOutcome(events []session.Event, turn int) TurnOutcome {
	o := TurnOutcome{Verdict: VerdictNoChanges, Why: "nothing here edited code"}
	edited := map[string]bool{}
	// Commands that failed earlier in the session, so a pass now is a
	// flip rather than a state that was already green.
	failedBefore := map[string]bool{}
	var lastText string
	testsEdited := map[string]bool{}
	ranTests, foundNothing, ranTheCode, selfAsserted := false, false, false, false
	// Commands seen in this turn, replayed once the edits are known so a
	// command naming an edited file counts however the two were ordered.
	var turnCmds []struct {
		cmd   string
		isErr bool
	}

	for _, e := range events {
		if e.Type == session.TypeAssistant && e.Turn == turn && e.Message != nil {
			if t := strings.TrimSpace(e.Message.Text()); t != "" {
				lastText = t
			}
		}
		if e.Type != session.TypeToolResult || e.CallID == "" {
			continue
		}
		cmd := commandOf(e.Arguments)
		check := e.Name == "bash" && isCheck(cmd)
		if e.Turn != turn {
			// Earlier in the session: remember what was red, so a pass in
			// this turn can be told from a check that was always green.
			if check && e.IsError {
				failedBefore[cmd] = true
			}
			continue
		}
		switch {
		case (e.Name == "write_file" || e.Name == "edit_file") && !e.IsError:
			if p := argPathOf(e.Arguments); p != "" {
				edited[p] = true
				if isTestFile(p) {
					testsEdited[p] = true
				}
			} else {
				edited[fmt.Sprintf("call:%s", e.CallID)] = true
			}
		case e.Name == "bash":
			turnCmds = append(turnCmds, struct {
				cmd   string
				isErr bool
			}{cmd, e.IsError})
			if !check {
				continue
			}
			o.Ran++
			o.Checks = append(o.Checks, textfmt.FirstLine(cmd, 70))
			switch {
			case e.IsError:
				o.Failed++
				failedBefore[cmd] = true
			default:
				o.Passed++
				if ran, nothing := exercised(e.Output); ran {
					ranTests = true
				} else if nothing {
					foundNothing = true
				} else if inlineAssertRe.MatchString(cmd) {
					selfAsserted = true
				}
				if failedBefore[cmd] {
					o.Flipped = true
				}
			}
		}
	}
	// A command that runs a file edited in this turn shows the code at
	// least executes. It is weaker than a test and stronger than nothing.
	for _, c := range turnCmds {
		if !c.isErr && ranEdited(c.cmd, edited) {
			ranTheCode = true
		}
	}
	o.Edited = len(edited)
	o.Tested = ranTests
	o.Executed = ranTheCode
	for p := range testsEdited {
		o.TestsEdited = append(o.TestsEdited, p)
	}
	sort.Strings(o.TestsEdited)

	switch {
	case o.Edited == 0 && o.Ran == 0:
		o.Verdict, o.Why = VerdictNoChanges, "nothing here edited code"
	case o.Edited == 0:
		o.Verdict, o.Why = VerdictNoChanges, fmt.Sprintf("nothing was edited; %d check(s) ran", o.Ran)
	case o.Failed > 0:
		o.Verdict = VerdictCheckFailed
		o.Why = fmt.Sprintf("%d file(s) edited and a check came back red", o.Edited)
	case o.Ran == 0 && !ranTheCode:
		o.Verdict = VerdictUnverified
		o.Why = fmt.Sprintf("%d file(s) edited and nothing was run against them", o.Edited)
	case o.Ran == 0:
		o.Verdict = VerdictNotDemonstrated
		o.Why = fmt.Sprintf("%d file(s) edited and the code was run, but nothing asserted the result", o.Edited)
	case ranTests && len(o.TestsEdited) > 0:
		// Green, but the suite that produced it was changed in the same
		// turn, so it shows the tests as they now stand pass — not that
		// the behaviour they used to check is intact.
		o.Verdict = VerdictSelfChecked
		o.Why = fmt.Sprintf("%d file(s) edited and tests passed, but %s changed in the same turn", o.Edited, strings.Join(o.TestsEdited, ", "))
	case ranTests && o.Flipped:
		o.Verdict = VerdictVerified
		o.Why = fmt.Sprintf("%d file(s) edited; tests that had been failing now pass", o.Edited)
	case ranTests:
		o.Verdict = VerdictVerified
		o.Why = fmt.Sprintf("%d file(s) edited and tests ran and passed afterwards", o.Edited)
	case selfAsserted:
		// Assertions the model wrote and ran in the same turn passed.
		// Weaker than the project's own tests, stronger than "it ran".
		o.Verdict = VerdictSelfChecked
		o.Why = fmt.Sprintf("%d file(s) edited and assertions written in the same turn passed; nothing already in the project checked it", o.Edited)
	case foundNothing:
		o.Verdict = VerdictVacuous
		o.Why = "a check passed having found no tests to run, so it shows nothing"
	default:
		o.Verdict = VerdictNotDemonstrated
		o.Why = fmt.Sprintf("%d file(s) edited and %d check(s) passed, but no test was exercised — it compiles, which is not the same as working", o.Edited, o.Passed)
	}
	if lastText != "" && claimRe.MatchString(lastText) {
		if m := claimRe.FindString(lastText); m != "" {
			o.Claimed = m
		}
	}
	return o
}

// Overclaimed reports whether the model asserted something the turn does
// not show. This is the case worth interrupting for: not a turn that left
// work unverified, but one that left it unverified and said otherwise.
func (o TurnOutcome) Overclaimed() bool {
	if o.Claimed == "" {
		return false
	}
	switch o.Verdict {
	case VerdictUnverified, VerdictCheckFailed, VerdictVacuous:
		return true
	}
	return false
}

// Correction is what the model is told when its claim and the turn
// disagree. It states the evidence and asks for the check, rather than
// accusing: the model may be right and simply not have shown it.
func (o TurnOutcome) Correction() string {
	var b strings.Builder
	fmt.Fprintf(&b, "[harness] You wrote %q, but this turn does not show it: %s.\n", o.Claimed, o.Why)
	switch o.Verdict {
	case VerdictUnverified:
		b.WriteString("Run the project's tests or build now and report the real result. If you cannot run them, say so plainly instead of implying they passed.")
	case VerdictNotDemonstrated:
		b.WriteString("Run something that asserts the result — the project's tests, or a command whose output shows the change behaves — and report what it said.")
	case VerdictCheckFailed:
		b.WriteString("A check came back red in this turn. Fix the cause and re-run it, or say that it is still failing.")
	case VerdictVacuous:
		b.WriteString("The command exited 0 but found no tests to run, so it demonstrates nothing. Run one that actually exercises the change.")
	}
	return b.String()
}

// Outcome is the verdict for the turn just finished.
func (a *Agent) Outcome() TurnOutcome {
	if a.Log == nil {
		return TurnOutcome{Verdict: VerdictNoChanges, Why: "no session log to judge from"}
	}
	return FoldOutcome(a.Log.Events(), a.turnN)
}

// logOutcome records the verdict so it is on the same trail as the turn
// it judges, and can be read back afterwards.
func (a *Agent) logOutcome(o TurnOutcome) {
	raw, err := json.Marshal(o)
	if err != nil {
		return
	}
	a.logEvent(session.Event{
		Type: session.TypeVerdict, Turn: a.turnN, Name: string(o.Verdict),
		Output: o.Why, IsError: !o.OK(), Extra: raw,
	})
}

// commandOf is the shell command of a bash call, for comparing one run
// with another.
func commandOf(arguments string) string {
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(arguments), &in) != nil {
		return ""
	}
	return strings.TrimSpace(in.Command)
}

// argPathOf is the file a write or edit named.
func argPathOf(arguments string) string {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(arguments), &in) != nil {
		return ""
	}
	return in.Path
}
