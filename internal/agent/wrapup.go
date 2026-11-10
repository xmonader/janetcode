package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
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
//
// The count restarts at an edit of a product file: the calls before it
// checked a change that is gone. On syn-kimi-final lean a-catalog-paging-s1
// the tests passed at call 4, the model's own check found a real tie-break
// bug at call 5 and it fixed it at call 6, and the note still went in at
// call 12 saying the change had been checked since call 4; the model cut
// its descending-cursor checks and the hidden suite failed on exactly that.

// wrapUpAfter is the calls after the turn first passed its checks at which
// the note goes in: pi's mean of 7.1 after-green calls, rounded up.
const wrapUpAfter = 8

// wrapState is the turn's count of calls made since VerifiedBy() was first
// non-empty or, after that, since the last edit of a product file, and
// whether the note has been given.
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
func (a *Agent) wrapUpNote(uses, results []llm.Block) string {
	if !a.WrapUp || a.Off["wrapup"] || a.wrap.fired || a.reviewSteps >= 0 || a.DryRunOn() {
		return ""
	}
	if a.wrap.seen {
		a.wrap.calls += len(uses)
		if editedProduct(uses, results) {
			a.wrap.calls = 0
		}
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
	return fmt.Sprintf("[harness] Your change passes `%s`, and you have spent %d calls checking it since your last change to it. Finish now: give your answer. Check one more thing only if a rule the task states is still unchecked, and name that rule.", by, a.wrap.calls)
}

// editedProduct reports whether one of the step's calls edited a file that
// is not a test, and succeeded.
func editedProduct(uses, results []llm.Block) bool {
	failed := map[string]bool{}
	for _, r := range results {
		if r.IsError {
			failed[r.ToolUseID] = true
		}
	}
	for _, u := range uses {
		if editTool(u.Name) && !failed[u.ID] {
			if p := inputPath(u.Input); p != "" && !isTestFile(p) {
				return true
			}
		}
	}
	return false
}
