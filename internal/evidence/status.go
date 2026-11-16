// Package evidence is the one model of verification evidence: what each
// check was, how it ended, whether it still speaks for the tree, and
// what the run as a whole lets anyone claim. The session log is the
// source (see FromEvents); the verdict fold, the proof notes, the
// headless result, /status and the turn line all read the same Report
// instead of keeping their own idea of "passed".
//
// The exit code is authoritative. Nothing here, and nothing an advisory
// classifier (the optional decision service) says, changes whether a
// command passed: an advisory is recorded beside the check, never in it.
package evidence

import (
	"regexp"
	"strings"
)

// Status is how one check ended, or what a set of checks add up to.
type Status string

const (
	// Passed: the command exited 0 and ran something.
	Passed Status = "passed"
	// Failed: the command ran and exited non-zero.
	Failed Status = "failed"
	// NotRun: the check was required and never started (an earlier
	// command in the gate failed, the gate was skipped, or there is no
	// check at all for work that needs one).
	NotRun Status = "not_run"
	// Cancelled: the run was interrupted before it finished.
	Cancelled Status = "cancelled"
	// Environmental: the machine, not the code, ended it: a missing
	// tool, an unreachable host, a full disk.
	Environmental Status = "environmental"
	// Flaky: the same command on an unchanged tree both passed and
	// failed, so neither result speaks for the code.
	Flaky Status = "flaky"
	// Inconclusive: it exited, but what it printed does not show
	// anything was checked (no tests found, timed out, backgrounded).
	Inconclusive Status = "inconclusive"
)

// All lists the statuses in the order a combined status prefers them:
// the first one present among fresh checks is the final status, and
// Passed is only the answer when nothing else is present.
var All = []Status{Failed, Cancelled, Environmental, Flaky, Inconclusive, NotRun, Passed}

// Valid reports whether s is one of the seven statuses.
func (s Status) Valid() bool {
	for _, v := range All {
		if s == v {
			return true
		}
	}
	return false
}

// Mark is the one-character gloss the turn line uses.
func (s Status) Mark() string {
	switch s {
	case Passed:
		return "✓"
	case Failed:
		return "✗"
	case Environmental, Flaky, Inconclusive, Cancelled:
		return "⚠"
	}
	return "—"
}

// Parse reads a status written by an older or foreign log; anything
// unknown is Inconclusive rather than a guess at passed or failed.
func Parse(s string) Status {
	if v := Status(s); v.Valid() {
		return v
	}
	return Inconclusive
}

// Exit codes the harness itself assigns.
const (
	// ExitTimeout is what a command killed for running too long reports
	// (the same 124 timeout(1) uses).
	ExitTimeout = 124
	// ExitUnknown is a failure whose exit code was not recorded.
	ExitUnknown = -1
)

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

// Exercised reports what a passing check's output shows. ran is true
// when tests executed; nothing is true when the runner said it found
// none. A Go run that reports "ok" for one package and "[no test files]"
// for another has exercised something, so the two are not opposites.
func Exercised(out string) (ran, nothing bool) {
	ran = ranRe.MatchString(out)
	nothing = !ran && nothingRe.MatchString(out)
	return ran, nothing
}

// environmentRe matches output that blames the machine: the tool, the
// network or the disk, not the code under test.
var environmentRe = regexp.MustCompile(`(?im)(command not found|executable file not found|is not recognized as an internal or external command|no such host|connection refused|temporary failure in name resolution|no space left on device|cannot allocate memory|read-only file system)`)

// Classify decides a finished command's status from what is recorded:
// the exit code and the output, plus whether it was interrupted. It is
// deterministic and looks only at the command's own result.
func Classify(exit int, output string, cancelled bool) Status {
	switch {
	case cancelled:
		return Cancelled
	case exit == ExitTimeout:
		return Inconclusive
	case exit == 126 || exit == 127:
		return Environmental
	case exit != 0:
		if environmentRe.MatchString(output) {
			return Environmental
		}
		return Failed
	}
	if _, nothing := Exercised(output); nothing {
		return Inconclusive
	}
	return Passed
}

var summaryRe = regexp.MustCompile(`(?i)(--- FAIL|\bFAIL\b|\bFAILED\b|\bpanic\b|\berror\b|\bpassed\b|\bPASS\b|^ok\b|timed out)`)

// maxSummary bounds an output summary: a line, not a log.
const maxSummary = 160

// Summarize is the one line of output worth showing for a check: the
// last line that says how it went, else the last line printed.
func Summarize(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	pick := ""
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.HasPrefix(l, "[exit code") {
			continue
		}
		if pick == "" {
			pick = l
		}
		if summaryRe.MatchString(l) {
			pick = l
			break
		}
	}
	if pick == "" {
		return "no output"
	}
	if r := []rune(pick); len(r) > maxSummary {
		pick = string(r[:maxSummary-1]) + "…"
	}
	return pick
}
