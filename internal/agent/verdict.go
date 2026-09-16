package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/gaming"
	"github.com/xmonader/janetcode/internal/project"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
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
	// VerdictTestsWritten: an /accept writing-phase turn wrote only test
	// files and saw them fail. Acceptance tests are red until the
	// implementation exists, so this is the phase doing its job, not a
	// failed check.
	VerdictTestsWritten Verdict = "tests written"
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
	// Gaming is report-only: signs the edits may have faked a green.
	// It never changes the verdict.
	Gaming []string `json:"gaming,omitempty"`
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
	case VerdictVerified, VerdictSelfChecked, VerdictNoChanges, VerdictTestsWritten:
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
	line := fmt.Sprintf("%s %s: %s", mark, o.Verdict, o.Why)
	if len(o.Gaming) > 0 {
		line += " · " + strings.Join(o.Gaming, "; ")
	}
	return line
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

// refusalPrefixes begin the tool results of calls the harness refused
// without running them (see authorize and toolGate). A log written before
// session.Event.Denied existed is recognised by these.
var refusalPrefixes = []string{DeniedMessage, CancelledMessage, "Denied by permission rule ", "Blocked by a hook: ", "Permission denied: no approver"}

// runFailed reports whether a bash result is a failed run. The bash tool
// does not mark a non-zero exit as an error — the model reads the
// "[exit code N]" line it appends — so the line is what says a check came
// back red, unless the tool noted that for this command exit 1 is an
// answer (grep finding nothing), not a failure.
func runFailed(e session.Event) bool {
	if e.IsError {
		return true
	}
	m := reExitCode.FindAllStringSubmatchIndex(e.Output, -1)
	if len(m) == 0 {
		return false
	}
	last := m[len(m)-1]
	switch code := e.Output[last[2]:last[3]]; {
	case code == "0":
		return false
	case code == "1" && tool.NoMatchMeaning(shell.CommandOf(e.Arguments), 1) != "":
		return false
	}
	return true
}

// refused reports whether a tool result is a refusal rather than a run.
func refused(e session.Event) bool {
	if e.Denied {
		return true
	}
	if !e.IsError {
		return false
	}
	for _, p := range refusalPrefixes {
		if strings.HasPrefix(e.Output, p) {
			return true
		}
	}
	return false
}

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

// isTestFile reports whether a path holds tests. A green produced by a
// suite that was itself edited in the same turn is weaker evidence than
// one against tests that were already there: at the honest end the model
// wrote a regression test with the fix, and at the other it changed the
// thing that was failing until it stopped failing. The harness cannot
// tell those apart, so it says which kind of green this is and leaves the
// judgement to the reader.
//
// The pattern itself lives in package tool (see project.IsTestFile), shared
// with internal/impact.
func isTestFile(path string) bool { return project.IsTestFile(path) }

// claimRe matches an assertion a check could have demonstrated. It is
// deliberately narrow: a claim about tests, builds or verification, not
// every use of the word "done".
var claimRe = regexp.MustCompile(`(?i)\b(tests? (now )?(pass|passes|passing|are green|succeed)|all tests? pass|build (succeeds|passes|is green|works)|verified|I (ran|have run) the tests?|checks? pass)\b`)

// FoldOutcome decides the verdict for one turn from its events. It reads
// only what the harness observed: which files were written, which
// commands ran, and how they exited. It is FoldOutcomeIn without a
// workspace root, for a caller that has none to give.
func FoldOutcome(events []session.Event, turn int) TurnOutcome {
	return FoldOutcomeIn(events, turn, "")
}

// maxCheckLen bounds a check command kept on the outcome: long enough to
// show a `go test -run 'A|B|C'` whole, short enough for a proof note.
const maxCheckLen = 240

// checkRun is one run of a check command within the turn being folded.
type checkRun struct {
	isErr  bool
	output string
	gate   string // the gate that ran it (session.GateVerify/GateAccept), "" for a model's bash call
}

// FoldOutcomeIn is FoldOutcome for a session working in root: a model's
// leading `cd <root> &&` is not part of the command, so the same command
// run by the model and by a gate is recognised as one. Each command is
// judged by its latest run in the turn — a red run fixed and re-run green
// (by the model or by a turn-end gate) is not a failure — and a call the
// harness refused never ran, so it is not a check at all.
func FoldOutcomeIn(events []session.Event, turn int, root string) TurnOutcome {
	o := TurnOutcome{Verdict: VerdictNoChanges, Why: "nothing here edited code"}
	edited := map[string]bool{}
	var deltas []gaming.Delta
	// Commands that failed earlier in the session, so a pass now is a
	// flip rather than a state that was already green.
	failedBefore := map[string]bool{}
	var lastText string
	testsEdited := map[string]bool{}
	acceptWriting := false
	// The latest run of each check command in this turn, in the order
	// each command last ran.
	latest := map[string]checkRun{}
	var order []string
	// Commands seen in this turn, replayed once the edits are known so a
	// command naming an edited file counts however the two were ordered.
	var turnCmds []struct {
		cmd   string
		isErr bool
	}
	see := func(cmd string, run checkRun) {
		if _, ok := latest[cmd]; ok {
			order = slices.DeleteFunc(order, func(c string) bool { return c == cmd })
		}
		order = append(order, cmd)
		latest[cmd] = run
	}

	for _, e := range events {
		switch {
		case e.Type == session.TypeTurnStart && e.Turn == turn:
			phase := strings.TrimPrefix(e.Reason, session.TurnStartAccept)
			acceptWriting = phase != e.Reason && (phase == string(session.AcceptWriting) || phase == string(session.AcceptProposed))
			continue
		case e.Type == session.TypeAssistant && e.Turn == turn && e.Message != nil:
			if t := strings.TrimSpace(e.Message.Text()); t != "" {
				lastText = t
			}
			continue
		case e.Type == session.TypeGate:
			cmd := shell.TrimWorkspaceCd(strings.TrimSpace(e.Name), root)
			if e.Turn != turn {
				if e.IsError {
					failedBefore[cmd] = true
				}
				continue
			}
			see(cmd, checkRun{isErr: e.IsError, output: e.Output, gate: e.Source})
			continue
		}
		if e.Type != session.TypeToolResult || e.CallID == "" || refused(e) {
			continue
		}
		cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
		check := e.Name == "bash" && shell.IsCheck(cmd)
		if e.Turn != turn {
			// Earlier in the session: remember what was red, so a pass in
			// this turn can be told from a check that was always green.
			if check && runFailed(e) {
				failedBefore[cmd] = true
			}
			continue
		}
		switch {
		case editTool(e.Name) && !e.IsError:
			if p := argPathOf(e.Arguments); p != "" {
				edited[p] = true
				if isTestFile(p) {
					testsEdited[p] = true
				}
			} else {
				edited[fmt.Sprintf("call:%s", e.CallID)] = true
			}
			if d := gamingDelta(e.Name, e.Arguments); d.Path != "" {
				deltas = append(deltas, d)
			}
		case e.Name == "bash":
			turnCmds = append(turnCmds, struct {
				cmd   string
				isErr bool
			}{cmd, runFailed(e)})
			if check {
				see(cmd, checkRun{isErr: runFailed(e), output: e.Output})
			}
		}
	}
	ranTests, foundNothing, ranTheCode, selfAsserted := false, false, false, false
	for _, cmd := range order {
		run := latest[cmd]
		o.Ran++
		o.Checks = append(o.Checks, textfmt.FirstLine(cmd, maxCheckLen))
		if run.isErr {
			o.Failed++
			continue
		}
		o.Passed++
		switch ran, nothing := exercised(run.output); {
		case ran, run.gate == session.GateAccept:
			// The locked acceptance tests passing is tests running, whether
			// or not the gate kept what they printed.
			ranTests = true
		case nothing:
			foundNothing = true
		case run.gate == session.GateVerify && run.output == "" && shell.IsTestRun(cmd):
			// A passing verify gate keeps no output; a test command in it
			// ran tests all the same.
			ranTests = true
		case inlineAssertRe.MatchString(cmd):
			selfAsserted = true
		}
		if failedBefore[cmd] {
			o.Flipped = true
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
	for _, s := range gaming.Scan(deltas) {
		o.Gaming = append(o.Gaming, s.String())
	}

	switch {
	case o.Edited == 0 && o.Ran == 0:
		o.Verdict, o.Why = VerdictNoChanges, "nothing here edited code"
	case o.Edited == 0:
		o.Verdict, o.Why = VerdictNoChanges, fmt.Sprintf("nothing was edited; %d check(s) ran", o.Ran)
	case o.Failed > 0 && acceptWriting && len(o.TestsEdited) == o.Edited:
		o.Verdict = VerdictTestsWritten
		o.Why = fmt.Sprintf("%d acceptance test file(s) written and failing as expected until the implementation exists", o.Edited)
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
	return FoldOutcomeIn(a.Log.Events(), a.turnN, a.displayRoot())
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

// argPathOf is the file a write or edit named.
func gamingDelta(name, arguments string) gaming.Delta {
	var in struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
		Content   string `json:"content"`
		Edits     []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	if json.Unmarshal([]byte(arguments), &in) != nil || in.Path == "" {
		return gaming.Delta{}
	}
	d := gaming.Delta{Path: in.Path}
	add := func(old, neu string) {
		if old != "" {
			d.Removed = append(d.Removed, strings.Split(old, "\n")...)
		}
		if neu != "" {
			d.Added = append(d.Added, strings.Split(neu, "\n")...)
		}
	}
	if name == "write_file" {
		add("", in.Content)
	} else {
		add(in.OldString, in.NewString)
		for _, e := range in.Edits {
			add(e.OldString, e.NewString)
		}
	}
	return d
}

func argPathOf(arguments string) string {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(arguments), &in) != nil {
		return ""
	}
	return in.Path
}
