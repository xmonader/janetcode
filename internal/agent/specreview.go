package agent

// A spec review sends a headless turn back once, when the model says it is
// done after changing files and its checks passed, to hold the change
// against the requirements the task states. On effbench the misses that
// remained were rules the public tests did not reach: a-catalog-paging
// validated min_price and max_price and never filtered by them, in four
// calls; c-go-cache let a concurrent Set exceed the capacity. pi made more
// calls and passed both. JANETCODE_OFF=specreview turns it off.

// specReviewNote is what the model is told when the review sends it back.
const specReviewNote = "[harness] Before you finish: go through the task description once more, requirement by requirement (every parameter, boundary, ordering, error case and concurrency rule it states), and check the ones the tests did not exercise against your code, with one inline script. Fix what is missing or wrong; if everything holds, say so in one line and stop. If a check of yours disagrees with the code, reread the task's words on that rule once and trust them, rather than debugging the check."

// maxReviewSteps is how many tool steps the review gets before the model
// is told to wrap up. On effbench r11 the review found real bugs in its
// first few calls and then spent up to twenty more debugging its own
// check scripts: about 150 s a run, which cost janetcode its time lead.
const maxReviewSteps = 6

// reviewWrapNote is attached to the results of the review's last step.
const reviewWrapNote = "[harness] The review has had its calls. Finish now: if your last change left the project's tests failing, fix that; otherwise stop and say what you changed."

// specReview reports whether the turn should go back for the review: on,
// not yet done this turn, and a turn that changed files and passed its
// checks.
func (a *Agent) specReview(outcome TurnOutcome, reviewed *bool, emit func(Event)) bool {
	if !a.SpecReview || a.Off["specreview"] || *reviewed {
		return false
	}
	if outcome.Verdict == VerdictNoChanges || !outcome.OK() {
		return false
	}
	*reviewed = true
	a.reviewSteps = 0
	emit(Event{Kind: EvNotice, Text: "the turn is done; asking the model to check the change against the task once"})
	a.appendHarnessText(specReviewNote)
	return true
}

// reviewStepNote counts a tool step of the review and returns the wrap-up
// note at its last one, "" otherwise.
func (a *Agent) reviewStepNote() string {
	if a.reviewSteps < 0 {
		return ""
	}
	a.reviewSteps++
	if a.reviewSteps == maxReviewSteps {
		return reviewWrapNote
	}
	return ""
}
