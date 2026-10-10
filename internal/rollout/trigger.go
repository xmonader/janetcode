package rollout

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

// End is how a run ended, as far as the retry cares: Clean, or the bad
// end that earns a checkpoint branch.
type End string

const (
	// EndClean is a run that finished on its own.
	EndClean End = ""
	// EndStuck is agent.ErrStuck: the loop detector stopped a run that
	// kept repeating the same call.
	EndStuck End = "stuck"
	// EndTurnLimit is agent.ErrMaxIterations: the turn ran out of model
	// calls.
	EndTurnLimit End = "turn limit"
	// EndToolLimit is agent.ErrToolLimit: --max-tool-calls ended the run.
	EndToolLimit End = "tool-call limit"
	// EndGateRed is a run whose last project check the harness ran (the
	// verify gate) failed: the gate sent it back verify.MaxRounds times
	// and gave up, or it stopped with the gate red.
	EndGateRed End = "gate red"
	// EndUntilRed is --until's check still failing after its rounds;
	// internal/headless reports it (the error is its own).
	EndUntilRed End = "until check red"
)

// BadEnd classifies a run's error and its events. Errors that are not a
// bad end of the run's own making — a cancellation, the budget, the wall
// clock, a provider that dropped the connection — are Clean here: a
// retry does not fix the network, and a ceiling the user set on money or
// time is not one to spend past.
func BadEnd(err error, events []session.Event) End {
	switch {
	case err == nil:
	case errors.Is(err, agent.ErrStuck):
		return EndStuck
	case errors.Is(err, agent.ErrMaxIterations):
		return EndTurnLimit
	case errors.Is(err, agent.ErrToolLimit):
		return EndToolLimit
	default:
		return EndClean
	}
	if GateRed(events) {
		return EndGateRed
	}
	return EndClean
}

// BadEndText is BadEnd for a recorded run, where only the error's text
// survives (a headless result's "error" field): the offline pre-screen
// reads runs this way.
func BadEndText(msg string, events []session.Event) End {
	switch {
	case msg == "":
	case strings.Contains(msg, agent.ErrStuck.Error()):
		return EndStuck
	case strings.Contains(msg, agent.ErrMaxIterations.Error()):
		return EndTurnLimit
	case strings.Contains(msg, agent.ErrToolLimit.Error()):
		return EndToolLimit
	default:
		return EndClean
	}
	if GateRed(events) {
		return EndGateRed
	}
	return EndClean
}

// GateRed reports whether the last check a harness gate ran in the run
// failed. A run with no gate at all is not red.
func GateRed(events []session.Event) bool {
	red := false
	for _, e := range events {
		if e.Type == session.TypeGate {
			red = e.IsError
		}
	}
	return red
}

// LastVerdict is the run's last recorded turn verdict, with ok false
// when it recorded none (it changed nothing, or ended before its verdict).
func LastVerdict(events []session.Event) (o agent.TurnOutcome, ok bool) {
	for _, e := range events {
		if e.Type != session.TypeVerdict || len(e.Extra) == 0 {
			continue
		}
		var v agent.TurnOutcome
		if json.Unmarshal(e.Extra, &v) == nil {
			o, ok = v, true
		}
	}
	return o, ok
}

// Signal is one more reason a green run's evidence may be weak, read from
// its events; it returns "" when it has nothing to say. It is the
// trigger's extension point: the probe ledger (no discriminating probe
// for a stated rule) and the test-integrity ledger plug in here, as
// deterministic folds of their own records, without this package having
// to know them.
type Signal func(events []session.Event) string

// Weak is why a run that finished green rests on weak evidence, empty
// when it does not: the second sample's trigger. The built-in reasons are
// the verdict's own — the model claimed a check the turn does not show
// (Overclaimed), or claimed one it then edited past (StaleCheck) — and a
// gaming signal in the turn's edits. A run without a verdict is not weak
// by this rule: it changed nothing, and a second sample of nothing is
// nothing.
func Weak(events []session.Event, extra ...Signal) []string {
	var why []string
	if o, ok := LastVerdict(events); ok {
		if o.Overclaimed() {
			why = append(why, "overclaimed: the model wrote "+quote(o.Claimed)+" and the turn was "+string(o.Verdict))
		}
		if o.StaleCheck() {
			why = append(why, "stale check: edited after the last check passed")
		}
		if len(o.Gaming) > 0 {
			why = append(why, "gaming signal: "+o.Gaming[0])
		}
	}
	for _, s := range extra {
		if s == nil {
			continue
		}
		if r := s(events); r != "" {
			why = append(why, r)
		}
	}
	return why
}

func quote(s string) string {
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return `"` + s + `"`
}
