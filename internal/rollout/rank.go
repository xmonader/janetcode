package rollout

import (
	"fmt"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

// Candidate is one attempt's facts, everything the selector ranks on.
// All of it is computed by the harness from the attempt's own record;
// none of it is the model's word.
type Candidate struct {
	// Name labels the attempt in the report: "first", "branch", "sample".
	Name string `json:"name"`
	// End is how the attempt ended (BadEnd).
	End End `json:"end,omitempty"`
	// Failed says the attempt ended on an error at all, a bad end or not.
	Failed bool `json:"failed,omitempty"`
	// GateRed is GateRed of its events.
	GateRed bool `json:"gate_red,omitempty"`
	// Verdict is its last turn verdict, when it recorded one.
	Verdict    agent.TurnOutcome `json:"-"`
	HasVerdict bool              `json:"-"`
	// Gaming is how many gaming signals its verdict carried.
	Gaming int `json:"gaming,omitempty"`
	// Waste is the share of its tool calls agent.WasteLabels labels as
	// waste, in thousandths, so ranking compares integers.
	Waste int `json:"waste_permille"`
	// Extra holds the scores later ledgers add, by name (see
	// ExtraProbeFailures, ExtraIntegrityFlags); lower is better, absent is
	// zero. Ranking reads them through ProbeKey and IntegrityKey.
	Extra map[string]int `json:"extra,omitempty"`
}

// The names Extra is keyed by. Nothing in this package sets them: the
// probe ledger (frozen probes the attempt fails) and the test-integrity
// ledger (pre-image test failures and copied-literal flags) fill them in
// when they exist, and the default ranking already reads them in the
// report's order.
const (
	ExtraProbeFailures  = "probe_failures"
	ExtraIntegrityFlags = "integrity_flags"
)

// NewCandidate folds one attempt's events and error into its facts.
func NewCandidate(name string, events []session.Event, err error) Candidate {
	c := Candidate{Name: name, End: BadEnd(err, events), Failed: err != nil, GateRed: GateRed(events)}
	c.fill(events)
	return c
}

// NewCandidateText is NewCandidate for a recorded run whose error only
// survives as text.
func NewCandidateText(name string, events []session.Event, errMsg string) Candidate {
	c := Candidate{Name: name, End: BadEndText(errMsg, events), Failed: errMsg != "", GateRed: GateRed(events)}
	c.fill(events)
	return c
}

func (c *Candidate) fill(events []session.Event) {
	c.Verdict, c.HasVerdict = LastVerdict(events)
	c.Gaming = len(c.Verdict.Gaming)
	labels := agent.WasteLabels(events, !c.Failed && !c.GateRed)
	if len(labels) > 0 {
		n := 0
		for _, l := range labels {
			if l != agent.WasteNone {
				n++
			}
		}
		c.Waste = n * 1000 / len(labels)
	}
}

// Key is one ranking criterion: Score maps a candidate to an integer,
// lower is better. Keys are compared in order and the first that differs
// decides; nothing is weighted or summed, so no criterion can be bought
// off by being good at a later one.
type Key struct {
	Name  string
	Score func(Candidate) int
}

// DefaultKeys is the ranking in order. The project's own checks first —
// did the attempt end on its own, is the gate green, does the verdict
// stand — then the slots the probe and integrity ledgers fill, then the
// gaming signals, and last the waste share, the overthinking-style
// tiebreak (choosing the less wasteful trajectory, arXiv 2502.08235).
func DefaultKeys() []Key {
	return []Key{
		{"end", func(c Candidate) int {
			switch {
			case c.End != EndClean:
				return 2
			case c.Failed:
				return 1
			}
			return 0
		}},
		{"gate", func(c Candidate) int { return b2i(c.GateRed) }},
		{"verdict", verdictScore},
		ProbeKey,
		IntegrityKey,
		{"gaming", func(c Candidate) int { return c.Gaming }},
		{"waste", func(c Candidate) int { return c.Waste }},
	}
}

// ProbeKey and IntegrityKey read the ledgers' scores out of Extra.
var (
	ProbeKey     = Key{"probes", func(c Candidate) int { return c.Extra[ExtraProbeFailures] }}
	IntegrityKey = Key{"integrity", func(c Candidate) int { return c.Extra[ExtraIntegrityFlags] }}
)

// verdictScore orders verdicts by how much they demonstrate: a verified
// turn whose claim stands, then any other passing verdict, then a weak
// one (overclaimed, stale, or a verdict that is not OK at all).
func verdictScore(c Candidate) int {
	if !c.HasVerdict {
		return 1
	}
	o := c.Verdict
	switch {
	case o.Overclaimed() || o.StaleCheck() || !o.OK():
		return 2
	case o.Verdict == agent.VerdictVerified:
		return 0
	}
	return 1
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Ranking is what Rank decided: the index of the winner, and the key
// that decided it ("" for a tie, which the first candidate wins).
type Ranking struct {
	Winner int
	By     string
	Why    string
}

// Rank picks the best of cands by keys (DefaultKeys when nil). A tie on
// every key goes to the earliest candidate: the first attempt's tree is
// already in place, and replacing it with one no better buys nothing.
func Rank(cands []Candidate, keys []Key) Ranking {
	if keys == nil {
		keys = DefaultKeys()
	}
	best := Ranking{}
	for i := 1; i < len(cands); i++ {
		for _, k := range keys {
			a, b := k.Score(cands[best.Winner]), k.Score(cands[i])
			if a == b {
				continue
			}
			if b < a {
				best = Ranking{Winner: i, By: k.Name, Why: fmt.Sprintf("%s: %s %d < %s %d", k.Name, cands[i].Name, b, cands[best.Winner].Name, a)}
			} else if best.By == "" {
				best.By, best.Why = k.Name, fmt.Sprintf("%s: %s %d < %s %d", k.Name, cands[best.Winner].Name, a, cands[i].Name, b)
			}
			break
		}
	}
	return best
}
