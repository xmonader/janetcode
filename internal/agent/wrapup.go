package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// A headless turn that keeps checking a change that already passes is told
// once to finish. On effbench (Kimi, syn-kimi-pi and syn-kimi-autocheck/off)
// janetcode reached its first passing public-test run after 8.6 tool calls
// (pi: 16.6) and then made 12.3 more (pi: 7.1): 60% of its calls came after
// the tests were green, mostly ad-hoc check scripts and test reruns, and
// d-todocli spent 36 calls and 1115 s there. Some of that checking finds
// hidden-test bugs, so this bounds it and does not ban it: the note lets the
// model go on when a rule of the task is still unchecked and it names the
// rule. A prompt rule and a harness-run check were both ignored by Kimi; a
// note injected on a tool result is not. JANETCODE_OFF=wrapup turns it off.

// wrapUpAfter is the calls after the turn first passed its checks at which
// the note goes in: pi's mean of 7.1 after-green calls, rounded up.
const wrapUpAfter = 8

// wrapState is the turn's count of calls made since VerifiedBy() was first
// non-empty, and whether the note has been given.
type wrapState struct {
	seen  bool // the turn has been verified at some step
	calls int
	fired bool
}

// wrapUpNote is called after a tool step of n calls ran. It counts the
// calls past the first green and returns the note when the count has
// reached wrapUpAfter and the state is green now (the last check passed and
// nothing was edited since), "" otherwise. It does not mark the note given:
// the caller does that once it has a result to attach it to, so a step that
// carries the review's own wrap-up note tries again on the next one. Steps
// of a spec review are not counted: the review has its own budget and its
// calls read code, they are not checks.
func (a *Agent) wrapUpNote(n int) string {
	if !a.WrapUp || a.Off["wrapup"] || a.wrap.fired || a.reviewSteps >= 0 || a.DryRunOn() {
		return ""
	}
	if a.wrap.seen {
		a.wrap.calls += n
	}
	by := a.VerifiedBy()
	if by != "" {
		a.wrap.seen = true
	}
	if by == "" || a.wrap.calls < wrapUpAfter {
		return ""
	}
	by, _, _ = strings.Cut(by, "\n") // an inline script: its first line names it
	by = textfmt.TruncateTo(by, 120)
	if !a.Off["wraprules"] {
		spec, checks := turnSpecAndChecks(a.Messages)
		if note := wrapRulesNote(by, a.wrap.calls, spec, checks); note != "" {
			return note
		}
	}
	return fmt.Sprintf("[harness] Your change passes `%s`, and you have spent %d calls checking it since it first passed. Finish now: give your answer. Check one more thing only if a rule the task states is still unchecked, and name that rule.", by, a.wrap.calls)
}
