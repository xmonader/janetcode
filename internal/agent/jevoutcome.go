package agent

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Session outcome classification (item 9 of the surviving list in
// docs/notes/jev.md), behind `decisions.session_outcome`, default off.
//
// It runs once, at the end of a session, off every hot path: by the
// time it is asked the work is over, so the only thing it can cost is
// the moment before the process exits. It answers one question — did
// this session get there — and the answer is logged as a decision event
// and offered to sleep consolidation, which today has no notion of how
// a session ended unless the model happened to open a goal.
//
// Nothing else reads it and nothing is denied because of it. The
// classification is of a session that is already over.

const qSessionOutcome = "session_outcome"

// Budgets for SessionOutcomeState: one line a turn, the whole session.
const (
	outcomePromptBudget = 220
	outcomeStateBudget  = 4000
	// outcomeTurnWindow is how many turns the state describes. A longer
	// session is rendered as its first and last turns with the count
	// between them — the ending is what the question is about, and the
	// opening is what it is measured against.
	outcomeTurnWindow = 24
)

// SessionOutcomeQuestions is the one question a finished session is
// asked. Exported so an offline replay scores the same thing a live
// session end does.
func SessionOutcomeQuestions() []jev.Question {
	return []jev.Question{
		{ID: qSessionOutcome, Kind: jev.Choice,
			Instructions: "How did this whole session end, judged only by the record above?",
			Options: map[string]string{
				"success":   "the session did what the user asked and the record shows it working or checked",
				"partial":   "some of what was asked was done, or it was done without ever being shown to work",
				"failed":    "the session tried and the record shows it did not get there",
				"abandoned": "the session stopped in the middle, without finishing and without saying it had",
			}},
	}
}

// SessionOutcomeState renders the session as one line a turn: what was
// asked, how much the agent did, and the harness's own verdict.
func SessionOutcomeState(turns []AnnotateTurn) string {
	var b strings.Builder
	b.WriteString("A coding agent session, one line per turn, oldest first.\n")
	head, tail := turns, []AnnotateTurn(nil)
	var dropped int
	if len(turns) > outcomeTurnWindow {
		keep := outcomeTurnWindow / 2
		head, tail = turns[:keep], turns[len(turns)-keep:]
		dropped = len(turns) - 2*keep
	}
	line := func(t AnnotateTurn) {
		b.WriteString("turn " + strconv.Itoa(t.Turn) + ": " +
			textfmt.Clip(textfmt.FirstLine(t.Prompt, outcomePromptBudget), outcomePromptBudget) +
			" | " + strconv.Itoa(len(t.Steps)) + " tool calls, " + strconv.Itoa(t.Failed) + " failed")
		if t.Verdict != "" {
			b.WriteString(" | verdict: " + textfmt.Clip(t.Verdict, outcomePromptBudget))
		}
		b.WriteString("\n")
	}
	for _, t := range head {
		line(t)
	}
	if dropped > 0 {
		b.WriteString("... " + strconv.Itoa(dropped) + " further turns omitted ...\n")
	}
	for _, t := range tail {
		line(t)
	}
	return textfmt.Clip(b.String(), outcomeStateBudget)
}

// SessionOutcomes are the four answers, in the order a reader expects
// them, for anything that needs to know the vocabulary without asking.
var SessionOutcomes = []string{"success", "partial", "failed", "abandoned"}

// ClassifySessionOutcome asks the question at the end of a session and
// records the answer. It is called once, after the last turn, and
// returns without doing anything at all when the consumer is off, no
// client is configured, the circuit is open or the session has no turns
// to describe — every one of which leaves the session exactly as it
// would have ended without the feature.
//
// The answer is logged and counted. It is not acted on: nothing in the
// harness changes because a session was called failed, and sleep
// consolidation reads it as one more fact about a record that is
// already written.
func (a *Agent) ClassifySessionOutcome(ctx context.Context) {
	dp := a.decisionsFor(DecisionOutcome)
	if dp == nil {
		return
	}
	if a.Log == nil || !dp.Available() {
		return
	}
	turns := AnnotateTurns(a.logEvents())
	if len(turns) == 0 {
		return
	}
	start := time.Now()
	res, err := dp.Evaluate(ctx, SessionOutcomeState(turns), SessionOutcomeQuestions())
	took := time.Since(start)
	cost, tokensOut := 0.0, 0
	if res != nil {
		cost, tokensOut = res.Cost, res.TokensOut
	}
	a.stats.decision(err != nil, took, cost, tokensOut)
	if err != nil || res == nil {
		if err != nil {
			a.decisionFailure("session_outcome", err, "error")
		}
		return
	}
	outcome, conf, ok := res.ChoiceAt(qSessionOutcome)
	if !ok {
		return
	}
	a.logEvent(session.Event{Type: session.TypeDecision, Turn: a.turnN,
		Name: "session_outcome", Extra: mustJSON(map[string]any{
			"consumer": "session_outcome",
			"model":    res.Model, "tokens_in": res.TokensIn,
			"tokens_out": res.TokensOut, "cost_usd": res.Cost,
			"latency_ms": res.Latency.Milliseconds(),
			"questions":  SessionOutcomeQuestions(),
			"answers":    res.Answers,
			"outcome":    outcome, "outcome_confidence": conf,
			"turns": len(turns),
			// Nothing changes because of this answer. Sleep consolidation
			// may read it; reading is not acting.
			"acted_on": false,
		})})
}
