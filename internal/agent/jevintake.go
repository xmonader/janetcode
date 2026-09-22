package agent

import (
	"context"
	"time"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Intake classification, Phase 3a of the Jev integration
// (docs/notes/jev-integration-spec.md): the session's first user prompt
// is classified — what kind of request it is, how involved it looks,
// whether it wants a plan — and the answer is logged and counted.
//
// Nothing reads it. Phase 3a is log-only by design: routing, plan mode
// and the todo hint keep their deterministic rules, and feeding intake
// answers into any of them is its own measured gate. Until then this
// consumer exists to produce the calibration data that gate needs.
//
// It also must not cost the turn anything. The call is made on its own
// goroutine, started before the turn's Prepare hook runs and collected
// once the turn's work is done, by which point a turn that did anything
// at all has outlived the call's own deadline; an answer still missing
// then is dropped rather than waited for.

// The intake question ids, stable because the session log, the offline
// replay and any later tuning read them.
const (
	qIntakeKind       = "kind"
	qIntakeComplexity = "complexity"
	qIntakeNeedsPlan  = "needs_plan"
)

// IntakeQuestions is what the first prompt is classified with, and what
// the offline replay scores. Exported for the replay driver so the two
// cannot drift apart.
func IntakeQuestions() []jev.Question {
	return []jev.Question{
		{ID: qIntakeKind, Kind: jev.Choice,
			Instructions: "What kind of request is this first message to a coding agent?",
			Options: map[string]string{
				"explain":   "the user wants to understand existing code or behaviour, with no change asked for",
				"implement": "the user wants new code, a new feature or a new file written",
				"debug":     "something is broken or failing and the user wants it found and fixed",
				"review":    "the user wants existing code judged: correctness, style, security, performance",
				"docs":      "the user wants prose written or updated: README, comments, manual, commit message",
				"other":     "none of the above, for instance a question about the tool itself or an ambiguous message",
			}},
		{ID: qIntakeComplexity, Kind: jev.Score,
			Instructions: "How much work is this request for an agent that can read and edit the repository? " +
				"Count reading as work: a question can cost as much as a change, because judging whether " +
				"something is correct, safe, fast or thread-safe means reading all of it first.",
			Levels: []string{
				"one look: one file read, or one fact, or a line the request quotes in full",
				"a small job: two or three files to read, or a few lines to change in one file",
				"a contained job: a handful of files that belong together, read or changed",
				"a real job: a whole subsystem to read through, or several files to change with tests",
				"a project: many files, migrations or design work that needs staging",
			}},
		{ID: qIntakeNeedsPlan, Kind: jev.Noul,
			Instructions: "Would this request go better if the agent wrote an explicit step plan before touching anything?",
			NoulCriteria: map[string]string{
				"true":  "the work has several steps that depend on each other, or an order the user would want to see first",
				"false": "the work is one step, or exploratory enough that a plan written now would be guesswork",
			}},
	}
}

// IntakeState renders the prompt Jev classifies. It is the user's text
// and nothing else: the classification is of the request, and anything
// else in the state would make it a classification of the repository.
// Exported so the replay corpus and a live session are built the same
// way. Scrubbing happens at the wire, as for every consumer.
func IntakeState(prompt string) string {
	return "Request: " + textfmt.Clip(prompt, 2000)
}

// intakeOutcome is one finished intake call, handed from the goroutine
// that made it to the turn goroutine that logs it. The result is never
// touched by both.
type intakeOutcome struct {
	res  *jev.Result
	err  error
	took time.Duration
}

// startIntake fires the classification for the session's first prompt,
// concurrently with the turn's preparation, and returns immediately.
// The turn never waits on it: drainIntake collects whatever has arrived
// by the end of the turn.
func (a *Agent) startIntake(ctx context.Context, prompt string) {
	dp := a.decisionsFor(DecisionIntake)
	if dp == nil || prompt == "" {
		return
	}
	if a.intakeCh != nil || a.intakeDone {
		return // one classification per session, and it is already away
	}
	if !dp.Available() {
		return
	}
	ch := make(chan intakeOutcome, 1)
	a.intakeCh = ch
	a.intakeDeadline = time.Now().Add(jev.MaxTimeout + intakeSlack)
	// context.WithoutCancel: a turn cancelled a moment after it starts
	// would otherwise leave a goroutine writing to a channel nobody
	// drains. The client's own timeout still bounds the call.
	callCtx := context.WithoutCancel(ctx)
	state := IntakeState(prompt)
	go func() {
		start := time.Now()
		res, err := dp.Evaluate(callCtx, state, IntakeQuestions())
		ch <- intakeOutcome{res: res, err: err, took: time.Since(start)}
	}()
}

// intakeSlack is how long past the client's own ceiling drainIntake
// will still wait for the goroutine's handoff. The client bounds the
// call itself; this covers only the send.
const intakeSlack = 250 * time.Millisecond

// drainIntake collects the classification at the end of the turn that
// asked for it, waiting at most until the moment the call itself must
// have finished — the client's ceiling plus the handoff. A first turn
// that did any real work has already outlived that, so in a session
// this waits for nothing at all; the bound exists so a turn that
// finished in microseconds still gets its answer instead of racing it.
// An answer that misses even that is dropped: Phase 3a is log-only, and
// a log-only consumer must never hold a turn open.
func (a *Agent) drainIntake() {
	if a.intakeCh == nil {
		return
	}
	done := func(out intakeOutcome) {
		a.intakeCh, a.intakeDone = nil, true
		a.recordIntake(out)
	}
	if left := time.Until(a.intakeDeadline); left > 0 {
		t := time.NewTimer(left)
		defer t.Stop()
		select {
		case out := <-a.intakeCh:
			done(out)
		case <-t.C:
			a.intakeCh = nil
		}
		return
	}
	select {
	case out := <-a.intakeCh:
		done(out)
	default:
		a.intakeCh = nil
	}
}

// recordIntake prices the call and writes the audit record. It runs on
// the turn goroutine, which is what makes the session log and the turn
// counters safe to touch here.
func (a *Agent) recordIntake(out intakeOutcome) {
	cost, tokensOut := 0.0, 0
	if out.res != nil {
		cost, tokensOut = out.res.Cost, out.res.TokensOut
	}
	a.stats.decision(out.err != nil, out.took, cost, tokensOut)
	if out.err != nil || out.res == nil {
		return
	}
	// awaitIntakeRouting has already decided by the time this runs, so the
	// record can say what the answer was used for rather than only what it
	// said. Routing off, or a late answer, leaves both fields empty.
	route, acted := "", false
	if a.DecisionsOn(DecisionRoute) {
		route = "default"
		if a.intakeRoute.Cheap {
			route, acted = "cheap", true
			a.stats.decisionActed()
		}
	}
	kind, kindConf, _ := out.res.ChoiceAt(qIntakeKind)
	complexity, _, complexityConf, _ := out.res.ScoreAt(qIntakeComplexity)
	needsPlan, _ := out.res.NoulAt(qIntakeNeedsPlan)
	rec := map[string]any{
		"consumer": "intake",
		"model":    out.res.Model, "tokens_in": out.res.TokensIn,
		"tokens_out": out.res.TokensOut, "cost_usd": out.res.Cost,
		"latency_ms": out.res.Latency.Milliseconds(),
		"questions":  IntakeQuestions(),
		"answers":    out.res.Answers,
		"kind":       kind, "kind_confidence": kindConf,
		"complexity": complexity, "complexity_confidence": complexityConf,
		"needs_plan": needsPlan,
		// Said plainly on the record so a later reader cannot mistake a
		// log-only classification for a decision the session acted on.
		"acted_on": acted,
	}
	// The routing consumer (jevintakeroute.go) is the only thing that
	// reads any of this. With it off the record carries no routing fields
	// at all, exactly as it did before the consumer existed.
	if route != "" {
		rec["route"], rec["route_reason"] = route, a.intakeRoute.Why
	}
	a.logEvent(session.Event{Type: session.TypeDecision, Turn: a.turnN, Name: "intake", Extra: mustJSON(rec)})
	a.stats.intake(kind)
}
