package agent

// A spec review sends a headless turn back once, when the model says it is
// done after changing files and its checks passed, to hold the change
// against the requirements the task states. On effbench the misses that
// remained were rules the public tests did not reach: a-catalog-paging
// validated min_price and max_price and never filtered by them, in four
// calls; c-go-cache let a concurrent Set exceed the capacity. pi made more
// calls and passed both. JANETCODE_OFF=specreview turns it off.

// specReviewNote is what the model is told when the review sends it back.
const specReviewNote = "[harness] Before you finish: go through the task description once more, requirement by requirement (every parameter, boundary, ordering, error case and concurrency rule it states), and check each one the tests did not exercise against your code, with one inline script. Fix what is missing or wrong; if everything holds, say so in one line and stop."

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
	emit(Event{Kind: EvNotice, Text: "the turn is done; asking the model to check the change against the task once"})
	a.appendHarnessText(specReviewNote)
	return true
}
