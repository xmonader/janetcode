package verify

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// A baseline is the project's checks run before a turn changes anything,
// so that the gate afterwards can tell a failure the turn caused from one
// that was already there. Without it, a repository whose tests were red
// before the agent touched it sends every turn back to "fix" them, round
// after round, at a model call each.

// CommandResult is one command's outcome in a full run (RunEach).
type CommandResult struct {
	Name   string
	OK     bool
	Exit   int
	Output string // a failure's redacted output; empty when it passed
	// Failures names what failed, when the output is one RunEach can
	// read (go test, pytest, jest, cargo test); Parsed says so.
	Failures []string
	Parsed   bool
	// FailLines counts lines that mention a failure or an error: the
	// fallback measure of "more broken than before" when nothing parsed.
	FailLines int
	// printed is what the command printed, redacted, pass or fail: kept
	// for Result's Runs (signed evidence hashes it), never serialised.
	printed string
	// Duration is how long the command ran; Cancelled that the run's
	// context cut it short.
	Duration  time.Duration
	Cancelled bool
}

// Report is a full run: every command, not only up to the first failure.
type Report struct {
	Commands []CommandResult
	Duration time.Duration
	// NotRun are the commands never started because the context ended.
	NotRun []string
}

// OK reports whether every command passed.
func (rep Report) OK() bool {
	for _, c := range rep.Commands {
		if !c.OK {
			return false
		}
	}
	return true
}

// Result is the Report in the shape the gate logs and reports: what
// passed, and the first command that failed.
func (rep Report) Result() Result {
	res := Result{Duration: rep.Duration}
	for _, c := range rep.Commands {
		res.Runs = append(res.Runs, Run{Name: c.Name, Exit: c.Exit, Output: c.printed, Duration: c.Duration, Cancelled: c.Cancelled})
		if c.Cancelled {
			res.Cancelled = true
		}
		if c.OK {
			res.Passed = append(res.Passed, c.Name)
		} else if res.Failed == "" {
			res.Failed, res.Exit, res.Output = c.Name, c.Exit, c.Output
		}
	}
	res.NotRun = rep.NotRun
	if len(rep.NotRun) > 0 {
		res.Cancelled = true
	}
	return res
}

// RunEach runs every command and records each one's outcome, without
// stopping at the first failure: a baseline has to know about the later
// commands too. Mutation testing is not part of it.
func (r *Runner) RunEach(ctx context.Context) Report {
	start := time.Now()
	var rep Report
	timeout := r.timeout()
	for i, command := range r.Config.Commands {
		if ctx.Err() != nil {
			for _, rest := range r.Config.Commands[i:] {
				rep.NotRun = append(rep.NotRun, r.name(rest))
			}
			break
		}
		began := time.Now()
		ok, exit, output := r.runOne(ctx, command, timeout)
		c := CommandResult{Name: r.name(command), OK: ok, Exit: exit, printed: output, Duration: time.Since(began), Cancelled: !ok && ctx.Err() != nil}
		if !ok {
			c.Output = output
			c.Failures, c.Parsed = ParseFailures(output)
			c.FailLines = countFailLines(output)
		}
		rep.Commands = append(rep.Commands, c)
	}
	rep.Duration = time.Since(start)
	return rep
}

var (
	goFailTest = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
	goFailPkg  = regexp.MustCompile(`(?m)^FAIL[ \t]+(\S+)[ \t]`)
	pytestFail = regexp.MustCompile(`(?m)^(?:FAILED|ERROR) (\S+)`)
	jestFail   = regexp.MustCompile(`(?m)^\s*● (.+?)\s*$`)
	cargoFail  = regexp.MustCompile(`(?m)^test (\S+) \.\.\. FAILED`)
	failLine   = regexp.MustCompile(`(?i)\b(fail|failed|failure|failures|error|errors|panic)\b`)
)

// ParseFailures names the failures in a test runner's output: go test
// (failing tests, and packages that failed without a failing test, as a
// build failure does), pytest, jest and cargo test. parsed is false when
// none of them matched, and the caller falls back to counting.
func ParseFailures(output string) (names []string, parsed bool) {
	set := map[string]bool{}
	add := func(re *regexp.Regexp, prefix string) {
		for _, m := range re.FindAllStringSubmatch(output, -1) {
			set[prefix+m[1]] = true
		}
	}
	add(goFailTest, "")
	add(goFailPkg, "package ")
	add(pytestFail, "")
	add(jestFail, "")
	add(cargoFail, "")
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, len(names) > 0
}

func countFailLines(output string) int {
	n := 0
	for _, l := range strings.Split(output, "\n") {
		if failLine.MatchString(l) {
			n++
		}
	}
	return n
}

// Delta is what a run changed against its baseline.
type Delta struct {
	// New is what the turn broke: a command that passed and now fails,
	// a failure that was not there before, or a command now printing more
	// failures than it did when nothing could be named.
	New []string
	// Pre is what was failing before the turn and still is.
	Pre []string
	// Fixed is what was failing before and passes now.
	Fixed []string
	// newCommands are the commands that hold anything in New, in order,
	// so the feedback shows their output.
	newCommands []CommandResult
}

// Regressed reports whether the turn made anything worse.
func (d Delta) Regressed() bool { return len(d.New) > 0 }

// Compare sets a run against the baseline taken before the turn.
func Compare(base, now Report) Delta {
	before := map[string]CommandResult{}
	for _, c := range base.Commands {
		before[c.Name] = c
	}
	var d Delta
	for _, c := range now.Commands {
		b, known := before[c.Name]
		switch {
		case c.OK:
			if known && !b.OK {
				d.Fixed = append(d.Fixed, c.Name)
			}
		case !known || b.OK:
			d.New = append(d.New, c.Name+" (passed before this turn, fails now)")
			d.newCommands = append(d.newCommands, c)
		case c.Parsed && b.Parsed:
			was := map[string]bool{}
			for _, f := range b.Failures {
				was[f] = true
			}
			fresh := false
			for _, f := range c.Failures {
				if was[f] {
					d.Pre = append(d.Pre, c.Name+": "+f)
				} else {
					d.New = append(d.New, c.Name+": "+f)
					fresh = true
				}
			}
			if fresh {
				d.newCommands = append(d.newCommands, c)
			}
		case c.FailLines > b.FailLines:
			d.New = append(d.New, fmt.Sprintf("%s (still failing, with more failures than before: %d failure lines, %d before)", c.Name, c.FailLines, b.FailLines))
			d.newCommands = append(d.newCommands, c)
		default:
			d.Pre = append(d.Pre, c.Name+" (was already failing before this turn)")
		}
	}
	return d
}

// Summary is a one-line account of a delta for notices.
func (d Delta) Summary() string {
	var parts []string
	if len(d.New) > 0 {
		parts = append(parts, textfmt.Plural(len(d.New), "new failure"))
	}
	if len(d.Pre) > 0 {
		parts = append(parts, fmt.Sprintf("%d failing before this turn too", len(d.Pre)))
	}
	if len(d.Fixed) > 0 {
		parts = append(parts, textfmt.Plural(len(d.Fixed), "check")+" fixed")
	}
	return strings.Join(parts, " · ")
}

// RegressionLead opens Feedback, so the gate can tell a regression's
// feedback from a Reminder.
const RegressionLead = "Verification failed, and this turn caused it."

// Reminder is what the model reads, once, when every failure was already
// there before the turn: not a repair order, since the turn did not cause
// them, but not silence either, since the request may be about them.
func (d Delta) Reminder() string {
	var b strings.Builder
	b.WriteString("The project's checks still fail, but every failure was already failing before this turn started (the same checks were run before your first change):\n")
	for _, p := range d.Pre {
		b.WriteString("- " + p + "\n")
	}
	if len(d.Fixed) > 0 {
		b.WriteString("\nNow passing that failed before: " + strings.Join(d.Fixed, ", ") + "\n")
	}
	b.WriteString("\nIf the request is about these failures, they are not fixed yet: keep going. If it is not, you did not cause them; finish, and say they were failing before.")
	return b.String()
}

// Feedback is what the model reads when the turn made things worse: what
// is new, the output of the commands that hold it, and what was already
// failing — named so the model leaves it alone rather than spending the
// round on it.
func (d Delta) Feedback() string {
	var b strings.Builder
	b.WriteString(RegressionLead + " Compared with the same checks run before you changed anything:\n\nNew failures:\n")
	for _, n := range d.New {
		b.WriteString("- " + n + "\n")
	}
	for _, c := range d.newCommands {
		fmt.Fprintf(&b, "\nCommand: %s\nExit code: %d\nOutput:\n```\n%s\n```\n", c.Name, c.Exit, textfmt.TruncateTo(strings.TrimSpace(c.Output), 6000/max(1, len(d.newCommands))))
	}
	if len(d.Pre) > 0 {
		b.WriteString("\nAlready failing before this turn (not caused by you; do not spend this round on them unless the request is about them):\n")
		for _, p := range d.Pre {
			b.WriteString("- " + p + "\n")
		}
	}
	b.WriteString("\nFix what is new, then finish. Do not disable or skip the check.")
	return b.String()
}
