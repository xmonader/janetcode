// Package dossier turns a failed run into a hand-off: a structured
// account a person (or a second run) can start from instead of starting
// over. It is folded from what the session already recorded — the event
// log and the provenance record — with no model call and nothing rerun:
//
//   - the smallest reproduction: the last red check, narrowed to the
//     tests it named when the runner is one whose single-test form is
//     known (go test -run, pytest node ids, cargo test --exact);
//   - the hypotheses tried: each batch of edits between two checks, with
//     the intent the model gave for them and the check that came after as
//     the evidence that ruled it out (or did not);
//   - the last red output, tail-clipped;
//   - the best state reached: the check with the fewest failing tests,
//     the change it came after, the changes made since, and a --what-if
//     pointer that replays the session up to that turn.
//
// A check is a bash call shell.IsCheck recognises, or a command a gate
// ran (session.TypeGate). A failing count is the failing test names the
// output lists (Go, pytest, jest/vitest, cargo, TAP), else a "N failed"
// summary, else 1 for a red run that names none.
package dossier

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

const (
	// redTail bounds the last red output kept, in bytes.
	redTail = 4 << 10
	// maxNarrow is the most test names a reproduction is narrowed to.
	maxNarrow = 5
	// maxHypotheses bounds the list; the oldest are dropped first and
	// Dropped says how many.
	maxHypotheses = 12
)

// Dossier is a failed run's hand-off.
type Dossier struct {
	SessionID string `json:"session_id"`
	// Task is the run's request, clipped; Outcome why the run counts as
	// failed (its error, or its last verdict).
	Task    string `json:"task,omitempty"`
	Outcome string `json:"outcome"`

	Repro      *Repro       `json:"repro,omitempty"`
	Hypotheses []Hypothesis `json:"hypotheses,omitempty"`
	Dropped    int          `json:"hypotheses_dropped,omitempty"`
	LastRed    *Check       `json:"last_red,omitempty"`
	Best       *Best        `json:"best,omitempty"`
	// Path is where the Markdown copy was written, when it was.
	Path string `json:"path,omitempty"`
}

// Repro is the smallest command known to show the failure.
type Repro struct {
	Command string   `json:"command"`
	Tests   []string `json:"tests,omitempty"`
	// Narrowed says Command was rewritten to run only Tests; false means
	// it is the check exactly as the run ran it.
	Narrowed bool `json:"narrowed"`
}

// Check is one check the run made, as the dossier folds it.
type Check struct {
	Turn    int      `json:"turn"`
	Command string   `json:"command"`
	Red     bool     `json:"red"`
	Failing int      `json:"failing"`
	Tests   []string `json:"tests,omitempty"`
	Output  string   `json:"output,omitempty"`
	// AfterChange is the provenance sequence number of the last change
	// made before the check, 0 for none.
	AfterChange int `json:"after_change"`
}

// Hypothesis is one batch of edits between checks: what was tried, and
// what the next check said about it.
type Hypothesis struct {
	N      int      `json:"n"`
	Turn   int      `json:"turn"`
	Files  []string `json:"files"`
	Intent []string `json:"intent,omitempty"`
	// Verdict is "ruled out" (the check after it was red), "worse" (red,
	// with more failing than before it), "held" (green) or "unchecked"
	// (no check came after it).
	Verdict  string `json:"verdict"`
	Evidence string `json:"evidence,omitempty"`
}

// Best is the best state the run reached.
type Best struct {
	Turn        int    `json:"turn"`
	AfterChange int    `json:"after_change"`
	Failing     int    `json:"failing"`
	Command     string `json:"command"`
	// Since are the changes made after it, the ones to look at first.
	Since []string `json:"since,omitempty"`
	// WhatIf replays the session up to the start of Turn and runs the
	// rest live: the nearest state janetcode can rebuild by itself.
	WhatIf string `json:"what_if"`
}

// Build folds a run's events and provenance record into its dossier.
// root makes paths relative; outcome is why the run failed.
func Build(id, task, outcome, root string, events []session.Event, changes []change.Change) Dossier {
	d := Dossier{SessionID: id, Task: textfmt.Clip(strings.TrimSpace(task), 300), Outcome: outcome}
	byCall := map[string][]change.Change{}
	for _, c := range changes {
		if !c.Undone {
			byCall[c.Call] = append(byCall[c.Call], c)
		}
	}
	calls := map[string]session.Event{}
	var (
		checks  []Check
		pending []change.Change
		lastSeq int
		hyps    []Hypothesis
	)
	closeHyp := func(ch *Check) {
		if len(pending) == 0 {
			return
		}
		h := Hypothesis{Turn: pending[0].Turn}
		seenF, seenI := map[string]bool{}, map[string]bool{}
		for _, c := range pending {
			p := rel(root, c.Path)
			if !seenF[p] {
				seenF[p] = true
				h.Files = append(h.Files, p)
			}
			if in := strings.TrimSpace(c.Intent); in != "" && !seenI[in] {
				seenI[in] = true
				h.Intent = append(h.Intent, in)
			}
		}
		switch {
		case ch == nil:
			h.Verdict = "unchecked"
		case !ch.Red:
			h.Verdict, h.Evidence = "held", fmt.Sprintf("`%s` passed", clipCmd(ch.Command))
		default:
			h.Verdict, h.Evidence = "ruled out", fmt.Sprintf("`%s` still red: %s", clipCmd(ch.Command), failingLine(*ch))
			if n := len(checks); n > 0 && checks[n-1].Failing < ch.Failing {
				h.Verdict = "worse"
				h.Evidence += fmt.Sprintf(" (was %d before it)", checks[n-1].Failing)
			}
		}
		hyps = append(hyps, h)
		pending = nil
	}
	addCheck := func(ch Check) {
		ch.AfterChange = lastSeq
		closeHyp(&ch)
		checks = append(checks, ch)
	}
	for _, e := range events {
		switch e.Type {
		case session.TypeToolCall:
			calls[e.CallID] = e
		case session.TypeFileChange:
			for _, c := range byCall[e.CallID] {
				if c.Path == e.Name || len(byCall[e.CallID]) == 1 {
					pending = append(pending, c)
					lastSeq = max(lastSeq, c.Seq)
					break
				}
			}
		case session.TypeToolResult:
			if e.Name != "bash" || session.IsNestedCall(e.CallID) {
				continue
			}
			args := e.Arguments
			if args == "" {
				args = calls[e.CallID].Arguments
			}
			cmd := shell.TrimWorkspaceCd(shell.CommandOf(args), root)
			if !shell.IsCheck(cmd) {
				continue
			}
			red, known := classify(e.Output, e.IsError)
			if !known {
				continue // neither an exit status nor a test summary: no evidence
			}
			addCheck(fold(e.Turn, cmd, red, e.Output))
		case session.TypeGate:
			addCheck(fold(e.Turn, e.Name, e.IsError, e.Output))
		}
	}
	closeHyp(nil)
	if len(hyps) > maxHypotheses {
		d.Dropped = len(hyps) - maxHypotheses
		hyps = hyps[d.Dropped:]
	}
	for i := range hyps {
		hyps[i].N = d.Dropped + i + 1
	}
	d.Hypotheses = hyps

	for i := len(checks) - 1; i >= 0; i-- {
		if checks[i].Red {
			c := checks[i]
			c.Output = textfmt.TailBytes(strings.TrimSpace(c.Output), redTail)
			d.LastRed = &c
			d.Repro = reproFor(c)
			break
		}
	}
	if b := best(checks); b != nil {
		for _, c := range changes {
			if !c.Undone && c.Seq > b.AfterChange {
				b.Since = appendUniq(b.Since, fmt.Sprintf("#%d %s%s", c.Seq, rel(root, c.Path), intentSuffix(c.Intent)))
			}
		}
		b.WhatIf = fmt.Sprintf("janetcode --what-if %s --from-turn %d", id, max(b.Turn, 1))
		d.Best = b
	}
	return d
}

func fold(turn int, cmd string, red bool, out string) Check {
	c := Check{Turn: turn, Command: cmd, Red: red, Output: out}
	if red {
		c.Tests = FailingTests(out)
		c.Failing = len(c.Tests)
		if c.Failing == 0 {
			c.Failing = summaryFailed(out)
		}
		if c.Failing == 0 {
			c.Failing = 1
		}
	}
	return c
}

// best is the check with the fewest failing tests, the latest among
// equals, as long as it is better than the last check (otherwise the end
// state is the best and there is nothing to point back to). A run with
// no check has no best state.
func best(checks []Check) *Best {
	if len(checks) == 0 {
		return nil
	}
	bi := 0
	for i, c := range checks {
		if c.Failing <= checks[bi].Failing {
			bi = i
		}
	}
	last := checks[len(checks)-1]
	if bi == len(checks)-1 || checks[bi].Failing >= last.Failing {
		return nil
	}
	c := checks[bi]
	return &Best{Turn: c.Turn, AfterChange: c.AfterChange, Failing: c.Failing, Command: c.Command}
}

var (
	reExit        = regexp.MustCompile(`\[exit code (\d+)\]`)
	reGoFail      = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)
	rePytestFail  = regexp.MustCompile(`(?m)^FAILED (\S+?)(?:\s+-\s.*)?$`)
	reJestFail    = regexp.MustCompile(`(?m)^\s*(?:✕|×|✗)\s+(.+?)(?:\s+\(\d+\s?m?s\))?\s*$`)
	reCargoFail   = regexp.MustCompile(`(?m)^test (\S+) \.\.\. FAILED`)
	reTAPFail     = regexp.MustCompile(`(?m)^not ok \d+ -?\s*(.+?)\s*$`)
	reSummaryFail = regexp.MustCompile(`(?i)\b(\d+) (?:failed|failing|failures?)\b`)
)

// classify reads a check's result: red when it exited non-zero, timed
// out, or its output names failing tests or a failed count (a model's
// `pytest | tail` exits 0 on a red suite); green when it exited 0 with
// none of that, or printed a passing summary. known is false for output
// that says neither, and for a command that never started (exit 127).
func classify(out string, isErr bool) (red, known bool) {
	code, ran := exitCode(out)
	if ran && code == 127 {
		return false, false
	}
	failing := len(FailingTests(out)) > 0 || summaryFailed(out) > 0
	switch {
	case isErr, ran && code != 0, failing, strings.Contains(out, "[command timed out"):
		return true, true
	case ran, rePassed.MatchString(out):
		return false, true
	}
	// The bash tool prints no footer for exit 0, so a check that printed
	// something and no failure is taken as having passed only when it
	// says so; anything else is not evidence.
	return false, false
}

var rePassed = regexp.MustCompile(`(?im)\b\d+ passed\b|\bok\s+\S+\s+[\d.]+s\b|^PASS\b|\btest result: ok\b`)

// exitCode reads the bash tool's own "[exit code N]" footer, the last one.
func exitCode(out string) (int, bool) {
	m := reExit.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	return n, err == nil
}

// FailingTests names the failing tests a red run's output lists, in
// order, without repeats.
func FailingTests(out string) []string {
	var names []string
	for _, re := range []*regexp.Regexp{reGoFail, rePytestFail, reCargoFail, reJestFail, reTAPFail} {
		for _, m := range re.FindAllStringSubmatch(out, -1) {
			names = appendUniq(names, strings.TrimSpace(m[1]))
		}
		if len(names) > 0 {
			break
		}
	}
	return names
}

func summaryFailed(out string) int {
	m := reSummaryFail.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(m[len(m)-1][1])
	return n
}

// reproFor narrows a red check to the tests it named, where the runner's
// one-test form is known; otherwise it is the check as run.
func reproFor(c Check) *Repro {
	r := &Repro{Command: c.Command, Tests: c.Tests}
	if len(c.Tests) == 0 || len(c.Tests) > maxNarrow {
		return r
	}
	cmd, ok := runnerOnly(c.Command)
	if !ok {
		return r
	}
	switch {
	case goTestRe.MatchString(cmd):
		base := goRunFlag.ReplaceAllString(cmd, "")
		top := map[string]bool{}
		var roots []string
		for _, t := range c.Tests {
			root, _, _ := strings.Cut(t, "/")
			if !top[root] {
				top[root] = true
				roots = append(roots, regexp.QuoteMeta(root))
			}
		}
		r.Command = strings.Join(strings.Fields(base), " ") + " -run '^(" + strings.Join(roots, "|") + ")$'"
		r.Narrowed = true
	case strings.Contains(cmd, "pytest"):
		head := cmd[:strings.Index(cmd, "pytest")+len("pytest")]
		r.Command = head + " -q " + strings.Join(quoteAll(c.Tests), " ")
		r.Narrowed = true
	case strings.HasPrefix(cmd, "cargo test") && len(c.Tests) == 1:
		r.Command = "cargo test " + c.Tests[0] + " -- --exact"
		r.Narrowed = true
	}
	return r
}

// runnerOnly is cmd as one test-runner invocation: with an output filter
// piped after it (`2>&1 | tail -5`) dropped, since narrowing the tests
// makes it moot, and ok false for anything else compound — a `&&` chain,
// a heredoc, a second command — whose rewrite could drop what it did.
func runnerOnly(cmd string) (string, bool) {
	cmd = strings.TrimSpace(cmd)
	if head, tail, piped := strings.Cut(cmd, "|"); piped {
		if !outputFilter.MatchString(strings.TrimSpace(tail)) {
			return "", false
		}
		cmd = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(head), "2>&1"))
	}
	if strings.ContainsAny(cmd, ";&<>`$") || strings.Contains(cmd, "\n") {
		return "", false
	}
	return cmd, true
}

var outputFilter = regexp.MustCompile(`^(tail|head|grep|cat|less|tee)\b[^|;&]*$`)

var (
	goTestRe  = regexp.MustCompile(`^go test\b`)
	goRunFlag = regexp.MustCompile(`\s-run[= ](?:'[^']*'|"[^"]*"|\S+)`)
)

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "'" + strings.ReplaceAll(x, "'", `'\''`) + "'"
	}
	return out
}

func failingLine(c Check) string {
	s := fmt.Sprintf("%d failing", c.Failing)
	if c.Failing == 1 && len(c.Tests) == 0 {
		s = "red"
	}
	if len(c.Tests) > 0 {
		shown := c.Tests
		if len(shown) > 4 {
			shown = shown[:4]
		}
		s += " (" + strings.Join(shown, ", ")
		if len(c.Tests) > 4 {
			s += ", ..."
		}
		s += ")"
	}
	return s
}

func clipCmd(cmd string) string { return textfmt.Clip(strings.Join(strings.Fields(cmd), " "), 80) }

func intentSuffix(in string) string {
	if in = strings.TrimSpace(in); in == "" {
		return ""
	}
	return " — " + textfmt.Clip(in, 100)
}

func rel(root, p string) string {
	if root != "" && strings.HasPrefix(p, root+"/") {
		return strings.TrimPrefix(p, root+"/")
	}
	return p
}

func appendUniq(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

// Empty reports a dossier with nothing in it beyond the outcome: no check
// was run and nothing was changed, so there is no hand-off to write.
func (d Dossier) Empty() bool {
	return d.Repro == nil && len(d.Hypotheses) == 0 && d.Best == nil
}

// JSON is the dossier as indented JSON.
func (d Dossier) JSON() ([]byte, error) { return json.MarshalIndent(d, "", "  ") }

// Markdown renders the dossier for a person.
func (d Dossier) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Failure dossier: %s\n\n", d.SessionID)
	if d.Task != "" {
		fmt.Fprintf(&b, "Task: %s\n\n", d.Task)
	}
	fmt.Fprintf(&b, "Outcome: %s\n\n", d.Outcome)
	b.WriteString("## Smallest reproduction\n\n")
	if d.Repro == nil {
		b.WriteString("No check came back red, so there is nothing to reproduce from the run's own record.\n\n")
	} else {
		fmt.Fprintf(&b, "```sh\n%s\n```\n\n", d.Repro.Command)
		if d.Repro.Narrowed {
			fmt.Fprintf(&b, "Narrowed from the run's last red check to %s.\n\n", textfmt.Plural(len(d.Repro.Tests), "failing test"))
		} else if len(d.Repro.Tests) > 0 {
			fmt.Fprintf(&b, "The run's last red check, as it ran it; failing: %s.\n\n", strings.Join(d.Repro.Tests, ", "))
		}
	}
	b.WriteString("## Hypotheses tried\n\n")
	if len(d.Hypotheses) == 0 {
		b.WriteString("The run changed nothing.\n\n")
	}
	if d.Dropped > 0 {
		fmt.Fprintf(&b, "(%s earlier not shown)\n\n", textfmt.Plural(d.Dropped, "hypothesis"))
	}
	for _, h := range d.Hypotheses {
		fmt.Fprintf(&b, "%d. turn %d, %s: **%s**", h.N, h.Turn, strings.Join(h.Files, ", "), h.Verdict)
		if h.Evidence != "" {
			fmt.Fprintf(&b, " — %s", h.Evidence)
		}
		b.WriteString("\n")
		for _, in := range h.Intent {
			fmt.Fprintf(&b, "   - %s\n", textfmt.Clip(in, 160))
		}
	}
	b.WriteString("\n## Last red output\n\n")
	if d.LastRed == nil {
		b.WriteString("None.\n\n")
	} else {
		fmt.Fprintf(&b, "Turn %d, `%s`:\n\n```\n%s\n```\n\n", d.LastRed.Turn, clipCmd(d.LastRed.Command), d.LastRed.Output)
	}
	b.WriteString("## Best state reached\n\n")
	if d.Best == nil {
		b.WriteString("The end state is as good as any the run checked.\n")
	} else {
		fmt.Fprintf(&b, "Turn %d, after change #%d: %s by `%s`.\n\n", d.Best.Turn, d.Best.AfterChange, textfmt.Plural(d.Best.Failing, "failing test"), clipCmd(d.Best.Command))
		if len(d.Best.Since) > 0 {
			b.WriteString("Changed since then:\n\n")
			for _, s := range d.Best.Since {
				fmt.Fprintf(&b, "- %s\n", s)
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Replay to that turn and continue: `%s`\n", d.Best.WhatIf)
	}
	return b.String()
}
