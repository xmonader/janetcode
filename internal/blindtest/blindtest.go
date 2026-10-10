// Package blindtest is the clean-room half of the blind tester: what a
// model that sees only the task is asked, how its reply is read into
// checks, and how a check's run is judged. The agent loop
// (internal/agent/blindtest.go) makes the call and runs the checks.
//
// The separation is the point. The spec review reads the implementer's
// code and shares its blind spots; a tester that never sees the code
// writes the checks a hidden test suite would, from the rules the task
// states, and the implementer is told only which rules failed, never the
// checks, so it cannot fit the code to them. On effbench every hidden
// test janetcode failed in r8-r13 and the held-out round checked a rule
// BRIEF.md states in words (see the commit that added this package).
package blindtest

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxChecks is how many checks one reply may hold; the rest are dropped.
// Each is a command the harness runs, so the bound is wall time.
const MaxChecks = 8

// MaxSpec bounds the task text the tester is sent, in bytes. It is one
// small-context call: the task, a file list, and the instructions.
const MaxSpec = 24 << 10

// Marker begins the line a check prints when the rule does not hold. A
// check that exits non-zero without it crashed — a wrong import, a name
// the task does not use, a syntax error — and says nothing about the
// code, so it is not reported.
const Marker = "VIOLATION:"

// System is the tester's system prompt.
const System = `You write hidden acceptance checks for a programming task. You see ONLY the task description and the project's file names, never the implementation. Another agent has implemented the task and its public tests pass; your checks look for the rules a thorough hidden test suite would check and the public tests may not: every parameter, boundary, ordering, error case, validation rule, message format and concurrency rule the task states.

Write at most 8 checks, one per rule, each against the public interface exactly as the task names it. For each, write:

RULE: <the rule, in one sentence, as the task states it>
` + "```sh" + `
<one shell command, run from the project root, that exercises only that rule>
` + "```" + `

Each command must exit 0 when the rule holds. When it does not hold it must print a line starting with "VIOLATION:" saying what was expected and what happened, and exit 1. Catch exceptions the code under test raises where the task says it must not raise, and report them as a VIOLATION. Use the project's language and standard tooling (python3 - <<'EOF' ... EOF, node -e, a throwaway _test.go file written with cat and run with go test -run, cargo test with a file under tests/). Do not install anything, do not use the network, and do not modify existing files. Check only what the task states; never guess at behaviour it leaves open. Do not deliberate at length: pick the clearest rules and start writing the checks at once.`

// Prompt is the tester's one user message: the task and the names of the
// project's files as the task found them.
func Prompt(spec string, files []string) string {
	if len(spec) > MaxSpec {
		spec = spec[:MaxSpec] + "\n[... task text cut]"
	}
	var b strings.Builder
	b.WriteString("TASK:\n")
	b.WriteString(strings.TrimSpace(spec))
	if len(files) > 0 {
		b.WriteString("\n\nPROJECT FILES (names only):\n")
		for _, f := range files {
			b.WriteString(f)
			b.WriteString("\n")
		}
	}
	b.WriteString("\nWrite the checks.")
	return b.String()
}

// Check is one rule the tester wrote a check for.
type Check struct {
	Rule string
	Cmd  string
}

// Parse reads the tester's reply: each "RULE:" line followed by the first
// fenced block after it. A rule with no block, an empty block, or a
// block before any rule is dropped; so is everything past MaxChecks.
func Parse(reply string) []Check {
	var out []Check
	lines := strings.Split(strings.ReplaceAll(reply, "\r\n", "\n"), "\n")
	rule := ""
	for i := 0; i < len(lines) && len(out) < MaxChecks; i++ {
		l := strings.TrimSpace(lines[i])
		if r, ok := cutRule(l); ok {
			rule = r
			continue
		}
		if !strings.HasPrefix(l, "```") || rule == "" {
			continue
		}
		var body []string
		j := i + 1
		for ; j < len(lines) && strings.TrimSpace(lines[j]) != "```"; j++ {
			body = append(body, lines[j])
		}
		i = j
		if cmd := strings.TrimSpace(strings.Join(body, "\n")); cmd != "" {
			out = append(out, Check{Rule: rule, Cmd: cmd})
		}
		rule = ""
	}
	return out
}

// numberedRuleRe matches a numbered rule label up to its colon.
var numberedRuleRe = regexp.MustCompile(`^(?i:rule)\s+\d+\s*(?:\*\*)?:(?:\*\*)?`)

// cutRule reads a "RULE: ..." line, markdown emphasis and list marks
// allowed around the label.
func cutRule(l string) (string, bool) {
	l = strings.TrimLeft(l, "-*# ")
	if n := strings.TrimLeft(l, "0123456789"); n != l && (strings.HasPrefix(n, ".") || strings.HasPrefix(n, ")")) {
		l = strings.TrimSpace(n[1:]) // "1. RULE:" / "2) RULE:"
	}
	l = strings.TrimPrefix(l, "**")
	// "RULE 3:" / "Rule 3:": a numbered label, as DeepSeek writes it.
	if m := numberedRuleRe.FindStringIndex(l); m != nil {
		l = "RULE:" + l[m[1]:]
	}
	for _, p := range []string{"RULE:", "Rule:", "RULE**:", "RULE:**"} {
		if r, ok := strings.CutPrefix(l, p); ok {
			r = strings.TrimSpace(strings.Trim(strings.TrimSpace(r), "*"))
			return r, r != ""
		}
	}
	return "", false
}

// Outcome is what one run of a check showed.
type Outcome int

const (
	// Inconclusive: the check could not run, timed out, or failed
	// without the marker — it says nothing about the code.
	Inconclusive Outcome = iota
	Held
	Violated
)

// Judge reads a run: ran says it ran to completion, failed that it exited
// non-zero. A run that printed the marker is a violation even when it
// exited 0, since the checker stated the rule broken.
func Judge(ran, failed bool, output string) Outcome {
	if !ran {
		return Inconclusive
	}
	if hasMarker(output) {
		return Violated
	}
	if failed {
		return Inconclusive
	}
	return Held
}

func hasMarker(output string) bool {
	for _, l := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), Marker) {
			return true
		}
	}
	return false
}

// Note is what the implementer is told: the rules whose checks failed,
// and nothing of the checks themselves.
func Note(rules []string) string {
	var b strings.Builder
	b.WriteString("[harness] An independent tester that saw only the task description, not your code, wrote checks for its rules. The checks for these rules failed against your code:\n")
	for i, r := range rules {
		fmt.Fprintf(&b, "%d. %s\n", i+1, r)
	}
	b.WriteString("For each, read what the task says about it and compare your code with that. Fix the code where it does not meet the rule. The tester can be wrong: if your code already meets the rule as the task states it, say so in one line and leave it. Run the project's tests once when you are done.")
	return b.String()
}
