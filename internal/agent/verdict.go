package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
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
)

// TurnOutcome is the verdict with what it was decided from, so the user
// and the model can both see the reasoning rather than a bare word.
type TurnOutcome struct {
	Verdict Verdict `json:"verdict"`
	Edited  int     `json:"edited"`  // files changed
	Ran     int     `json:"ran"`     // check commands run
	Passed  int     `json:"passed"`  // of those, that exited 0
	Failed  int     `json:"failed"`  // of those, that did not
	Flipped bool    `json:"flipped"` // a command that had failed now passes
	// Checks are the commands counted as checks, newest last.
	Checks []string `json:"checks,omitempty"`
	// Why is the one-line reason for the verdict.
	Why string `json:"why"`
	// Claimed is what the model asserted about verification, when it
	// asserted anything a check could have shown.
	Claimed string `json:"claimed,omitempty"`
}

// OK reports whether the turn ended in a state worth trusting.
func (o TurnOutcome) OK() bool {
	return o.Verdict == VerdictVerified || o.Verdict == VerdictNoChanges
}

// Line renders the verdict for the user, in one line.
func (o TurnOutcome) Line() string {
	mark := "—"
	switch o.Verdict {
	case VerdictVerified:
		mark = "✓"
	case VerdictCheckFailed, VerdictVacuous:
		mark = "✗"
	case VerdictUnverified, VerdictNotDemonstrated:
		mark = "⚠"
	}
	return fmt.Sprintf("%s %s: %s", mark, o.Verdict, o.Why)
}

// vacuousRe matches the ways a check says it exercised nothing while
// still exiting 0. A green that collected no tests proves nothing, and it
// is the easiest way for a change to look done when it is not.
var vacuousRe = regexp.MustCompile(`(?i)(no tests? (ran|were run|to run|found|collected)|0 tests?|ran 0 tests|no test files|collected 0 items|no tests executed|empty test suite|matched no files)`)

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
	vacuous := false

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
		check := e.Name == "bash" && testCommandRe.MatchString(cmd)
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
			} else {
				edited[fmt.Sprintf("call:%s", e.CallID)] = true
			}
		case check:
			o.Ran++
			o.Checks = append(o.Checks, firstLineOf(cmd))
			switch {
			case e.IsError:
				o.Failed++
				failedBefore[cmd] = true
			default:
				o.Passed++
				if vacuousRe.MatchString(e.Output) {
					vacuous = true
				}
				if failedBefore[cmd] {
					o.Flipped = true
				}
			}
		}
	}
	o.Edited = len(edited)

	switch {
	case o.Edited == 0 && o.Ran == 0:
		o.Verdict, o.Why = VerdictNoChanges, "nothing here edited code"
	case o.Edited == 0:
		o.Verdict, o.Why = VerdictNoChanges, fmt.Sprintf("nothing was edited; %d check(s) ran", o.Ran)
	case o.Failed > 0:
		o.Verdict = VerdictCheckFailed
		o.Why = fmt.Sprintf("%d file(s) edited and a check came back red", o.Edited)
	case o.Ran == 0:
		o.Verdict = VerdictUnverified
		o.Why = fmt.Sprintf("%d file(s) edited and nothing was run against them", o.Edited)
	case vacuous:
		o.Verdict = VerdictVacuous
		o.Why = "a check passed without exercising anything (no tests collected)"
	case o.Flipped:
		o.Verdict = VerdictVerified
		o.Why = fmt.Sprintf("%d file(s) edited; a check that had been failing now passes", o.Edited)
	default:
		o.Verdict = VerdictNotDemonstrated
		o.Why = fmt.Sprintf("%d file(s) edited and %d check(s) passed, but none of them was failing first", o.Edited, o.Passed)
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
	case VerdictCheckFailed:
		b.WriteString("A check came back red in this turn. Fix the cause and re-run it, or say that it is still failing.")
	case VerdictVacuous:
		b.WriteString("The command exited 0 but collected no tests, so it demonstrates nothing. Run one that actually exercises the change.")
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

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 70 {
		s = s[:69] + "…"
	}
	return s
}
