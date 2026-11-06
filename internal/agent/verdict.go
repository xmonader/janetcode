package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
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

// FlippedWhy ends the reason of a verified turn whose tests had been red
// in an earlier turn: a real fix, not a first build going green.
const FlippedWhy = "tests that had been failing now pass"

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
	// VerdictDryRun: the turn ran in a dry run, so nothing it edited
	// reached the disk and nothing it would have run ran. A simulated
	// command is no check, and the ordinary fold would count one as a
	// pass.
	VerdictDryRun Verdict = "dry run"
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
	// EditedAfterCheck is true when the last successful edit of the turn
	// came after the last check that ran: whatever passed, it passed
	// against a tree the turn has since changed. Ran and Passed are
	// per-turn totals and cannot say this.
	EditedAfterCheck bool `json:"edited_after_check,omitempty"`
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
	case VerdictVerified, VerdictSelfChecked, VerdictNoChanges, VerdictTestsWritten, VerdictDryRun:
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
// prose, is what a build log says about itself and is not evidence. A
// count must be non-zero: "test result: ok. 0 passed", "Ran 0 tests" and
// "0 examples" are a runner saying it found nothing, and read as tests
// they made an empty suite verify a turn. Go's "ok  pkg" line is matched
// in lower case only, or JUnit's "OK (0 tests)" read as one.
var ranRe = regexp.MustCompile(`(?im)((?-i:^ok)\s+\S+|^PASS$|\b[1-9]\d* passed|\b[1-9]\d* tests?,|test result: ok\. [1-9]|Tests:\s+[1-9]|OK \([1-9]\d* test|Ran [1-9]\d* test|\b[1-9]\d* examples?, \d+ failures|` +
	// node --test (spec "ℹ pass N", TAP "# pass N"), mocha, zig, ctest,
	// bazel, dart — each carries the runner's own count or shape, since a
	// bare "All tests passed!" is what a model's own script prints.
	`^(?:ℹ|#) pass [1-9]\d*$|\b[1-9]\d* passing\b|^All \d+ tests passed\.$|\d+% tests passed|Executed [1-9]\d* out of \d+ tests?|\+[1-9]\d*: All tests passed!)`)

// nothingRe matches a runner saying it found nothing to run. A green that
// collected no tests is the easiest way for a change to look done when
// nothing checked it.
var nothingRe = regexp.MustCompile(`(?im)(no tests? (ran|were run|were found|to run|found|collected|executed)|collected 0 items|ran 0 tests|0 passed|empty test suite|no test suites found|^(?:ℹ|#) tests 0$|\b0 passing\b)`)

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
	case code == "1" || code == "141":
		n, _ := strconv.Atoi(code)
		return tool.NoMatchMeaning(shell.CommandOf(e.Arguments), n) == ""
	}
	return true
}

// notFoundRe matches a shell saying the command itself is missing. Such
// a run produced no verdict on the code: `python -m pytest ...` on a box
// where the binary is python3 is a typo, and counting it red kept a turn
// that went on to run the very same suite green reported as a failure.
var notFoundRe = regexp.MustCompile(`(?im)^.*(command not found|: not found|No such file or directory)\s*$`)

// neverRan reports whether a bash result is a command that never started:
// exit 127 with the shell saying so. It is not a check that came back red.
func neverRan(e session.Event) bool {
	if e.IsError {
		return false
	}
	m := reExitCode.FindAllStringSubmatchIndex(e.Output, -1)
	if len(m) == 0 {
		return false
	}
	last := m[len(m)-1]
	return e.Output[last[2]:last[3]] == "127" && notFoundRe.MatchString(e.Output)
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
func ranEdited(cmd string, edited map[string]bool, root string) bool {
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
		if runsPackageOf(cmd, path, root) {
			return true
		}
	}
	return false
}

// runsPackageOf reports whether cmd runs the package an edited file is in
// without naming the file: `go run .` or `go run ./cmd/x` for a Go file in
// that directory (`./...` for any), `cargo run` for any Rust file. The
// model's usual way of running what it changed counted as nothing, and the
// turn read "nothing was run against it" over output that showed it ran.
func runsPackageOf(cmd, path, root string) bool {
	if root != "" && filepath.IsAbs(path) {
		if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
	}
	dir := filepath.ToSlash(filepath.Dir(filepath.Clean(path)))
	for _, elem := range strings.FieldsFunc(cmd, func(r rune) bool { return r == ';' || r == '&' || r == '|' || r == '\n' }) {
		w := shell.Words(elem)
		switch {
		case len(w) >= 2 && w[0] == "cargo" && w[1] == "run":
			if strings.HasSuffix(path, ".rs") {
				return true
			}
		case len(w) >= 2 && w[0] == "go" && w[1] == "run" && strings.HasSuffix(path, ".go"):
			for _, a := range w[2:] {
				if strings.HasPrefix(a, "-") {
					continue
				}
				if strings.HasSuffix(a, ".go") {
					break // files named: the basename check covers them
				}
				target := filepath.ToSlash(filepath.Clean(a))
				if rest, ok := strings.CutSuffix(target, "..."); ok {
					rest = strings.TrimSuffix(rest, "/")
					if rest == "" || rest == "." || dir == rest || strings.HasPrefix(dir, rest+"/") {
						return true
					}
				} else if dir == target {
					return true
				}
				break // the first package argument is the program
			}
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

// docsOnly reports whether every file edited is prose: markdown, rst,
// asciidoc, or a licence-style file. Plain .txt is left out on purpose —
// requirements.txt and CMakeLists.txt are build inputs.
func docsOnly(edited map[string]bool) bool {
	for p := range edited {
		if !isDocFile(p) {
			return false
		}
	}
	return len(edited) > 0
}

func isDocFile(path string) bool {
	base := filepath.Base(path)
	switch strings.ToLower(filepath.Ext(base)) {
	case ".md", ".markdown", ".rst", ".adoc":
		return true
	case ".txt":
		// Prose or a scratch file, which no test exercises, except the
		// dependency lists that happen to be .txt.
		l := strings.ToLower(base)
		return !strings.HasPrefix(l, "requirements") && !strings.HasPrefix(l, "constraints")
	}
	stem := strings.ToUpper(strings.TrimSuffix(base, filepath.Ext(base)))
	switch stem {
	case "LICENSE", "LICENCE", "COPYING", "NOTICE", "AUTHORS", "CHANGELOG":
		return true
	}
	return false
}

// claimRe matches an assertion a check could have demonstrated. It is
// deliberately narrow: a claim about tests, builds or verification, not
// every use of the word "done".
var claimRe = regexp.MustCompile(`(?i)\b(tests? (now )?(pass|passes|passing|are green|succeed)|all tests? pass|build (succeeds|passes|is green|works)|verified|I (ran|have run) the tests?|checks? pass)\b`)

// checkTarget is what a check command is aimed at: the last argument of
// its check segment that is not a flag, a flag's value, a redirection or
// a shell operator — the test file, the package pattern, the directory.
// Two commands with the same target are two ways of running the same
// check. Only the check's own segment counts: the whole command's last
// word made `go test ./pkg/ | head` aim at "head" and `go test ./pkg/ -run
// TestX | sed -n 1,80p` at "1,80p", so the green `go test ./pkg/ | tail
// -3` after them never retired them and a finished turn was sent back
// (realrepo caddy-fmt).
func checkTarget(cmd string) string {
	t, sel := checkAim(cmd)
	if t == "" || shell.IsTestRun(t) {
		// `go test -run TestX`: the selection is all that names it.
		if sel != "" {
			return sel
		}
	}
	return t
}

// checkAim is checkTarget's two parts: the target, and the selection a
// flag such as -run or -k narrowed it to ("" for the whole target).
func checkAim(cmd string) (target, selection string) {
	seg := cmd
	for _, s := range shell.Segments(cmd) {
		if shell.IsCheck(s) {
			seg = s
		}
	}
	var skip string
	for _, w := range shell.Words(seg) {
		switch {
		case skip != "":
			if selectFlag[skip] {
				selection = w
			}
			skip = ""
			continue
		case flagTakesValue[w]:
			skip = w
			continue
		case w == "", strings.HasPrefix(w, "-"),
			strings.ContainsAny(w, "<>"), w == "&&", w == "||", w == "|", w == ";":
			continue
		}
		target = w
	}
	return target, selection
}

// selectFlag lists the flags of flagTakesValue that pick which tests run.
var selectFlag = map[string]bool{"-run": true, "-skip": true, "-bench": true, "-k": true}

// flagTakesValue lists the test runners' flags whose next word is a value,
// not the target: `go test ./pkg -run TestX` is aimed at ./pkg.
var flagTakesValue = map[string]bool{
	"-run": true, "-skip": true, "-count": true, "-timeout": true, "-tags": true,
	"-bench": true, "-parallel": true, "-cpu": true, "-p": true, "-k": true, "-m": true,
}

// supersededBy reports whether a green run of later retires an earlier red
// run of cmd: the same target, exercised the same way (both run tests or
// neither does), written differently. `python -m pytest t.py` fixed to
// `python3 -m pytest t.py`, or `pip install -e . && python -m mutest
// tests/` fixed to `.venv/bin/mutest tests/`, is one check that ended
// green, not one red check and one green one. A different kind of check
// over the same target — `go build ./...` then `go test ./...` — is not,
// because the build failure is its own answer.
func supersededBy(cmd, later string, scratch func(string) bool) bool {
	if cmd == later {
		return false
	}
	// A scratch check the model wrote for itself (see the scratch
	// predicate) that came back red, followed by one that came back green,
	// is the model fixing its own check: its wrong expectation, not the
	// code. Holding the red one against the turn sent a finished turn back
	// for a round of re-checking (effbench b-report-paging: two extra
	// calls after every test had passed; a-build-incremental: four).
	if scratch != nil && scratch(cmd) && scratch(later) {
		return true
	}
	t, sel := checkAim(cmd)
	lt, lsel := checkAim(later)
	if t == "" && sel == "" || t != lt || shell.IsTestRun(cmd) != shell.IsTestRun(later) {
		return false
	}
	// The later run covers the earlier one when it ran the whole target
	// or the same selection: a green `-run TestB` says nothing of TestA.
	return lsel == "" || lsel == sel
}

// scratchRe is an interpreter fed a script inline: `python3 - <<'EOF'`,
// `node -e '...'`, `python -c "..."`.
var scratchRe = regexp.MustCompile(`\b(python3?|node|ruby|perl|bash|sh|deno|bun)\b(\s+-\s*)?\s*(<<-?\s*['"]?\w+|-[ce]\s)`)

// scriptExtRe is a file an interpreter runs.
var scriptExtRe = regexp.MustCompile(`\.(py|js|mjs|cjs|ts|rb|sh|pl)$`)

// runsScript reports whether some word of cmd names a script file for
// which is returns true.
func runsScript(cmd string, is func(word string) bool) bool {
	for _, w := range shell.Words(cmd) {
		if scriptExtRe.MatchString(w) && is(w) {
			return true
		}
	}
	return false
}

// scratchScript reports whether a command runs a script written inline.
func scratchScript(cmd string) bool { return scratchRe.MatchString(cmd) }

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

// checkLabel is how a check command is recorded on the outcome. The first
// line is not enough: five different `python3 -c "` scripts, each a
// separate check on a real session, were all recorded as the four
// characters before their first newline, and the record said nothing
// about what had run. A multi-line command is folded onto one line
// instead, so what is kept is the beginning of the script rather than the
// beginning of its first line.
func checkLabel(cmd string) string {
	if strings.ContainsAny(cmd, "\n\r\t") {
		cmd = strings.Join(strings.Fields(cmd), " ")
	}
	return textfmt.FirstLine(cmd, maxCheckLen)
}

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
	// Checks that came back red in this turn before anything was edited:
	// failing when the turn found them, so run red, fix, run green is a
	// fix. Red after the turn's own edits is its own breakage repaired.
	redHere := map[string]bool{}
	editedYet := false
	var lastText string
	testsEdited := map[string]bool{}
	// Every test file written this turn, added to or changed.
	testsTouched := map[string]bool{}
	// Files written for the first time in this turn: what they hold is
	// this turn's work, not something that was already there.
	createdHere := map[string]bool{}
	acceptWriting := false
	// The sequence numbers of the last successful edit and the last check
	// that ran in this turn, so the fold can say whether anything was run
	// after the last change rather than only how much ran in total.
	lastEditSeq, lastCheckSeq := 0, 0
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
			if e.IsError && !editedYet {
				redHere[cmd] = true
			}
			see(cmd, checkRun{isErr: e.IsError, output: e.Output, gate: e.Source})
			lastCheckSeq = e.Seq
			continue
		}
		if e.Type != session.TypeToolResult || e.CallID == "" || refused(e) {
			continue
		}
		cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
		check := e.Name == "bash" && shell.IsCheck(cmd)
		if e.Turn != turn {
			// Earlier in the session: remember what was red, so a pass in
			// this turn can be told from a check that was always green. A
			// command that never started is not red, here as in this turn.
			if check && runFailed(e) && !neverRan(e) {
				failedBefore[cmd] = true
			}
			continue
		}
		switch {
		case editTool(e.Name) && !e.IsError:
			if p := argPathOf(e.Arguments); p != "" {
				edited[p] = true
				if e.Name == "write_file" && strings.HasPrefix(e.Output, "Created ") {
					createdHere[p] = true
				}
				if isTestFile(p) {
					testsTouched[p] = true
					// A test added — a new file, or new cases around the
					// ones already there — raises the bar; only a change
					// to lines that were there before can lower it.
					if !createdHere[p] && !additiveEdit(e.Name, e.Arguments) {
						testsEdited[p] = true
					}
				}
			} else {
				edited[fmt.Sprintf("call:%s", e.CallID)] = true
			}
			lastEditSeq = e.Seq
			editedYet = true
			if d := gamingDelta(e.Name, e.Arguments); d.Path != "" {
				d.Created = createdHere[d.Path]
				deltas = append(deltas, d)
			}
		case e.Name == "bash":
			turnCmds = append(turnCmds, struct {
				cmd   string
				isErr bool
			}{cmd, runFailed(e)})
			if check && !neverRan(e) {
				if runFailed(e) && !editedYet {
					redHere[cmd] = true
				}
				see(cmd, checkRun{isErr: runFailed(e), output: e.Output})
				lastCheckSeq = e.Seq
			}
		}
	}
	// The one thing Ran and Passed cannot say: whether anything was run
	// after the last edit. A turn that edits, tests, edits again and stops
	// has a green check and a stale one.
	o.EditedAfterCheck = lastEditSeq > 0 && lastEditSeq > lastCheckSeq
	// Scratch: a script written inline, one this turn created, or one
	// outside the project — the model's own check, not the project's.
	scratch := func(cmd string) bool {
		return scratchScript(cmd) || runsScript(cmd, func(w string) bool {
			if createdHere[strings.TrimPrefix(w, "./")] {
				return true
			}
			return filepath.IsAbs(w) && root != "" && !strings.HasPrefix(w, root+string(filepath.Separator))
		})
	}
	// A red check that a later green check re-ran another way never
	// reached the code: the model fixed the command, not the code.
	retired := map[string]bool{}
	for i, cmd := range order {
		if !latest[cmd].isErr {
			continue
		}
		for _, later := range order[i+1:] {
			if !latest[later].isErr && supersededBy(cmd, later, scratch) {
				retired[cmd] = true
				break
			}
		}
	}
	order = slices.DeleteFunc(order, func(cmd string) bool { return retired[cmd] })
	ranTests, foundNothing, ranTheCode, selfAsserted := false, false, false, false
	for _, cmd := range order {
		run := latest[cmd]
		o.Ran++
		o.Checks = append(o.Checks, checkLabel(cmd))
		if run.isErr {
			o.Failed++
			// A suite that ran and came back red still ran. Reporting
			// Tested=false there told the model it had not tested
			// anything, when what it had was a failing test — and a
			// project whose own example suite ships one deliberate
			// failure (a testing library's demo) could never be tested.
			if ran, _ := exercised(run.output); ran {
				ranTests = true
			}
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
		if failedBefore[cmd] || redHere[cmd] {
			o.Flipped = true
		}
	}
	// A command that runs a file edited in this turn shows the code at
	// least executes. It is weaker than a test and stronger than nothing.
	for _, c := range turnCmds {
		if !c.isErr && ranEdited(c.cmd, edited, root) {
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
		o.Verdict, o.Why = VerdictNoChanges, fmt.Sprintf("nothing was edited; %s ran", textfmt.Plural(o.Ran, "check"))
	case docsOnly(edited):
		// A README or a licence has nothing a test could exercise; warning
		// that the edit was "not demonstrated" is noise the user learns
		// to ignore, and then ignores when it matters.
		o.Verdict, o.Why = VerdictNoChanges, fmt.Sprintf("only documentation or text was edited (%s); nothing to test", textfmt.Plural(o.Edited, "file"))
	case o.Failed > 0 && acceptWriting && len(testsTouched) == o.Edited:
		o.Verdict = VerdictTestsWritten
		o.Why = fmt.Sprintf("%s written and failing as expected until the implementation exists", textfmt.Plural(o.Edited, "acceptance test file"))
	case o.Failed > 0:
		o.Verdict = VerdictCheckFailed
		o.Why = fmt.Sprintf("%s edited and a check came back red", textfmt.Plural(o.Edited, "file"))
	case o.Ran == 0 && !ranTheCode:
		o.Verdict = VerdictUnverified
		them := "them"
		if o.Edited == 1 {
			them = "it"
		}
		o.Why = fmt.Sprintf("%s edited and nothing was run against %s", textfmt.Plural(o.Edited, "file"), them)
	case o.Ran == 0:
		o.Verdict = VerdictNotDemonstrated
		o.Why = fmt.Sprintf("%s edited and the code was run, but nothing asserted the result", textfmt.Plural(o.Edited, "file"))
	case ranTests && len(o.TestsEdited) > 0:
		// Green, but the suite that produced it was changed in the same
		// turn, so it shows the tests as they now stand pass — not that
		// the behaviour they used to check is intact.
		o.Verdict = VerdictSelfChecked
		o.Why = fmt.Sprintf("%s edited and tests passed, but %s changed in the same turn", textfmt.Plural(o.Edited, "file"), strings.Join(o.TestsEdited, ", "))
	case ranTests && o.Flipped:
		o.Verdict = VerdictVerified
		o.Why = fmt.Sprintf("%s edited; %s", textfmt.Plural(o.Edited, "file"), FlippedWhy)
	case ranTests:
		o.Verdict = VerdictVerified
		o.Why = fmt.Sprintf("%s edited and tests ran and passed afterwards", textfmt.Plural(o.Edited, "file"))
	case selfAsserted:
		// Assertions the model wrote and ran in the same turn passed.
		// Weaker than the project's own tests, stronger than "it ran".
		o.Verdict = VerdictSelfChecked
		o.Why = fmt.Sprintf("%s edited and assertions written in the same turn passed; nothing already in the project checked it", textfmt.Plural(o.Edited, "file"))
	case foundNothing:
		o.Verdict = VerdictVacuous
		o.Why = "a check passed having found no tests to run, so it shows nothing"
	default:
		o.Verdict = VerdictNotDemonstrated
		o.Why = fmt.Sprintf("%s edited and %s passed, but no test was exercised — it compiles, which is not the same as working", textfmt.Plural(o.Edited, "file"), textfmt.Plural(o.Passed, "check"))
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

// StaleCheck reports the premature stop the verdict rules cannot see: a
// turn that ran a check, edited afterwards, and then said it was done.
// Every total the fold keeps is green — something ran, it passed, tests
// were exercised — and none of it was run against the tree as it now
// stands.
//
// It is deliberately narrower than Overclaimed: the model must have
// claimed something, and the turn must actually have checked at some
// point, because a turn that never ran anything is already
// VerdictUnverified and already nagged for it.
func (o TurnOutcome) StaleCheck() bool {
	return o.Claimed != "" && o.EditedAfterCheck && o.Ran > 0 && !o.Overclaimed()
}

// StaleCheckNote is what the model is told. Same voice as Correction:
// state the evidence, ask for the check, leave room for being wrong.
func (o TurnOutcome) StaleCheckNote() string {
	return fmt.Sprintf("[harness] You wrote %q, and %d check(s) did pass in this turn — but you edited again afterwards, "+
		"so nothing has been run against the code as it now stands. Re-run the check and report what it says, "+
		"or say plainly that the last edit is unverified.", o.Claimed, o.Passed)
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
	if a.DryRunOn() {
		files, calls := len(a.dryFiles()), 0
		for _, x := range a.DryRunActions() {
			if x.Kind != "file" && x.Kind != "network" {
				calls++
			}
		}
		return TurnOutcome{Verdict: VerdictDryRun, Why: fmt.Sprintf("nothing was changed or run: %d file(s) previewed, %d call(s) recorded · /dryrun show", files, calls)}
	}
	if a.Log == nil {
		return TurnOutcome{Verdict: VerdictNoChanges, Why: "no session log to judge from"}
	}
	return FoldOutcomeIn(a.Log.Events(), a.turnN, a.displayRoot())
}

// VerifiedBy is the check that passed after the turn's last edit so far,
// or "" when the turn has edited nothing, changed files after its last
// check, or saw that check fail.
func (a *Agent) VerifiedBy() string {
	o := a.Outcome()
	if o.Verdict != VerdictVerified || o.EditedAfterCheck || len(o.Checks) == 0 {
		return ""
	}
	return o.Checks[len(o.Checks)-1]
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

// gamingDelta is the lines an edit or write call removed and added, for
// the gaming scan; zero when the call names no path.
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

// additiveEdit reports whether an edit_file call only added text: every
// replacement keeps the text it found. A write over an existing file, a
// line-range edit or an insertion with nothing located cannot be told
// apart from a rewrite, so they count as changes.
func additiveEdit(name, arguments string) bool {
	if name != "edit_file" {
		return false
	}
	var in struct {
		oneEditArgs
		Edits []oneEditArgs `json:"edits"`
	}
	if json.Unmarshal([]byte(arguments), &in) != nil {
		return false
	}
	edits := in.Edits
	if in.Old != "" || in.byLine() {
		edits = append(edits, in.oneEditArgs)
	}
	if len(edits) == 0 {
		return false
	}
	for _, e := range edits {
		if e.byLine() || e.Old == "" || !strings.Contains(e.New, e.Old) {
			return false
		}
	}
	return true
}

type oneEditArgs struct {
	Old        string `json:"old_string"`
	New        string `json:"new_string"`
	From       int    `json:"from_line"`
	To         int    `json:"to_line"`
	FromAnchor string `json:"from_anchor"`
	ToAnchor   string `json:"to_anchor"`
}

func (e oneEditArgs) byLine() bool {
	return e.From > 0 || e.To > 0 || e.FromAnchor != "" || e.ToAnchor != ""
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
