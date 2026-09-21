package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/verify"
)

// Verify-failure triage, Phase 1 of the Jev integration
// (docs/notes/jev-integration-spec.md): after a gate failure, ask the
// optional decision provider three typed questions and use the answers
// to skip a repair round that would very likely burn a full model turn
// on an environmental, flaky or unrelated failure.
//
// The contract, restated where it matters: the exit code stays
// authoritative — Jev never reinterprets what passed or failed. A wrong
// "environmental" answer costs today's behaviour minus one repair round
// (the round is still counted), and a wrong "patch-caused" answer
// changes nothing at all. Any error, timeout, open circuit or absent
// provider is today's behaviour, byte for byte.

// DecisionProvider is what Agent.Decisions implements: one Evaluate
// call answers every question against one state. Available is a cheap
// gate checked before any state is built.
type DecisionProvider interface {
	Evaluate(ctx context.Context, state string, questions []jev.Question) (*jev.Result, error)
	Available() bool
}

// TriageQuestions is the offline calibration replay's question set
// (cmd/janetcode --decisions-replay): the live set a turn asks, plus
// worth_repair, which the replay records per row while the live
// mapping ignores it. Built here rather than copied there so the two
// cannot drift apart.
func TriageQuestions() []jev.Question {
	return append(triageQuestions(), jev.Question{ID: qWorthRepair, Kind: jev.Noul,
		Instructions: "Is another automated repair attempt on this failure likely to succeed?"})
}

// TriageSkip reports the live mapping's consequential answer for a
// result: the failure class and whether a repair round would be
// skipped. Exported for the same reason.
func TriageAction(res *jev.Result) (class string, skip bool) {
	v := triageAction(res)
	return v.class, v.skip
}

// The triage question ids. They are stable: they are the keys the
// session log, the offline replay and any future threshold tuning
// read. qWorthRepair is asked by the replay only (TriageQuestions).
const (
	qCausedByChange = "caused_by_change"
	qFailureClass   = "failure_class"
	qWorthRepair    = "worth_repair"
)

// triageQuestions is what a live gate failure asks: the two answers
// triageAction actually reads, evaluated together in one request. They
// are independent by design (a System One request evaluates them in
// parallel against the same state). A question the mapping ignores is
// state and tokens spent on every gate failure for nothing, so
// worth_repair stays in the replay set until something reads it.
func triageQuestions() []jev.Question {
	return []jev.Question{
		{ID: qCausedByChange, Kind: jev.Noul,
			Instructions: "Is this failure plausibly caused by the change just made, rather than by the environment, a flaky test or an unrelated cause?",
			NoulCriteria: map[string]string{
				"true":  "the failing output points at code, files or behaviour the change touched",
				"false": "the failure is a missing dependency, tool or service, a test that passed before on the same code or moves between runs, or a failure on a path the change did not touch",
			}},
		{ID: qFailureClass, Kind: jev.Choice,
			Instructions: "What kind of verification failure is this?",
			Options: map[string]string{
				"patch_caused":  "the change the agent just made broke it: the failure names code, files or behaviour the change touched",
				"environmental": "the machine rather than the code: a dependency, tool, service or runtime that is missing, unreachable or the wrong version",
				"flaky":         "the failure is not deterministic: it passed on a retry or earlier on this same code, or it comes from timing, a race, a port or another shared resource in the test setup rather than from the code under test",
				"unrelated":     "a pre-existing failure on a path the change did not touch",
				"inconclusive":  "the output is not enough to tell which of the others it is",
			}},
	}
}

// failure classes that justify skipping a repair round.
var skipClasses = map[string]bool{"environmental": true, "flaky": true, "unrelated": true}

// triage thresholds. Tuned from nothing so far — the offline replay
// (Phase 0) is what earns or replaces these numbers; they are table
// constants here so the mapping stays pure and exhaustively testable.
const (
	// A patch-caused probability this high means the ordinary path:
	// send the failure back to the model.
	triageCaused = 0.7
	// Below this, with a confident skip-class answer, the round is
	// skipped.
	triageNotCaused = 0.3
	// How sure the class answer must be before it overrides the
	// default of trying to repair.
	triageClassConfidence = 0.7
)

// triageState renders what Jev is asked to judge. Deliberately small:
// the request, the failing command with its (already bounded) output,
// and the turn's changed files as names with line counts — not diffs,
// which blow the state up and land on the model's counting weakness.
//
// Nothing is scrubbed here. There is exactly one scrubbing hook and it
// is at the wire (jev.Client.Scrub), which covers every field of every
// consumer's state — including the failing command line itself, which a
// per-field hook here used to miss.
func (a *Agent) triageState(res verify.Result) string {
	var b strings.Builder
	req := a.integ.userText
	b.WriteString("Request: " + textfmt.Clip(req, 1500) + "\n")
	b.WriteString("Failing check: " + res.Failed + "\n")
	if out := strings.TrimSpace(res.Output); out != "" {
		b.WriteString("Output: " + textfmt.Clip(out, 1200) + "\n")
	}
	if files := a.turnChangedFiles(); len(files) > 0 {
		b.WriteString("Changed this turn: " + strings.Join(files, ", ") + "\n")
	}
	return b.String()
}

// turnChangedFiles summarises this turn's provenance: path (tool +add/-del),
// aggregated per file, newest tool last.
func (a *Agent) turnChangedFiles() []string {
	type agg struct {
		tool     string
		add, del int
	}
	byPath := map[string]*agg{}
	var order []string
	for _, c := range a.Changes() {
		if c.Turn != a.turnN {
			continue
		}
		e := byPath[c.Path]
		if e == nil {
			e = &agg{}
			byPath[c.Path] = e
			order = append(order, c.Path)
		}
		e.tool = c.Tool
		add, del := c.Stats()
		e.add, e.del = e.add+add, e.del+del
	}
	sort.Strings(order)
	out := make([]string, 0, len(order))
	for _, p := range order {
		e := byPath[p]
		rel := p
		if a.Root != "" && strings.HasPrefix(p, a.Root+string([]byte{'/'})) {
			rel = p[len(a.Root)+1:]
		}
		out = append(out, fmt.Sprintf("%s (%s +%d/-%d)", rel, e.tool, e.add, e.del))
	}
	return out
}

// triageVerdict is the decision the mapping produces.
type triageVerdict struct {
	skip  bool   // true: end the turn without sending the model back
	class string // the class answer, for the notice
	p     float64
	conf  float64
}

// triageAction is the pure mapping from answers to behaviour, so it can
// be tested exhaustively without a provider.
func triageAction(res *jev.Result) triageVerdict {
	p, okP := res.NoulAt(qCausedByChange)
	class, conf, okC := res.ChoiceAt(qFailureClass)
	v := triageVerdict{class: class, p: p, conf: conf}
	switch {
	case !okP || !okC:
		return v // incomplete answer: today's behaviour
	case p >= triageCaused:
		return v
	case p <= triageNotCaused && skipClasses[class] && conf >= triageClassConfidence:
		v.skip = true
		return v
	default:
		return v
	}
}

// triageFailure asks the decision provider about a failed gate and
// reports whether the repair round should be skipped. Every soft
// failure returns false, which is "send the failure back as always".
// The one session-level notice — the circuit opening — is emitted here,
// on the turn goroutine that made the failing call, exactly once per
// session however often the circuit re-opens after a failed probe: the
// first notice already says the feature paused itself.
func (a *Agent) triageFailure(ctx context.Context, res verify.Result, emit func(Event)) bool {
	if a.Decisions == nil || !a.DecisionsTriage || a.Off["decisions"] {
		return false
	}
	if !a.Decisions.Available() {
		return false
	}
	start := time.Now()
	ans, err := a.Decisions.Evaluate(ctx, a.triageState(res), triageQuestions())
	cost, tokensOut := 0.0, 0
	if ans != nil {
		cost, tokensOut = ans.Cost, ans.TokensOut
	}
	a.stats.decision(err != nil, time.Since(start), cost, tokensOut)
	if err != nil {
		if !a.Decisions.Available() && !a.decisionCircuitSeen {
			a.decisionCircuitSeen = true
			emit(Event{Kind: EvNotice, Text: fmt.Sprintf("decisions: repeated errors, paused for %v and then retried once (%s)", jev.DefaultCooldown, err.Error())})
		}
		return false
	}
	// The circuit can also open on latency, after a call that succeeded:
	// ten medians past a second is the service, not this session.
	if !a.Decisions.Available() && !a.decisionCircuitSeen {
		a.decisionCircuitSeen = true
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("decisions: median latency past %v over %d calls, paused for %v and then retried once", jev.LatencyOpenP50, jev.LatencyWindow, jev.DefaultCooldown)})
	}
	v := triageAction(ans)
	a.logEvent(session.Event{Type: session.TypeDecision, Turn: a.turnN,
		Name: "verify_triage", Extra: mustJSON(map[string]any{
			"consumer": "verify_triage",
			"model":    ans.Model, "tokens_in": ans.TokensIn, "tokens_out": ans.TokensOut,
			"cost_usd": ans.Cost, "latency_ms": ans.Latency.Milliseconds(),
			"questions": triageQuestions(),
			"answers":   ans.Answers,
			"class":     v.class, "caused_by_change": v.p, "confidence": v.conf, "skip": v.skip,
		})})
	what := fmt.Sprintf("decisions: failure looks %s (p(caused)=%.2f, confidence=%.2f)", v.class, v.p, v.conf)
	if v.skip {
		emit(Event{Kind: EvNotice, Text: what + " · not sending the model back"})
		return true
	}
	emit(Event{Kind: EvNotice, Text: what + " · sending the model back"})
	return false
}
