package eval

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// Sequential testing: an A/B that may stop as soon as the evidence is
// decisive, without the inflated false-positive rate that "peek after
// every round and stop when p < 0.05" carries. Compare's bootstrap is a
// fixed-sample test: its p-value means what it says only if the number of
// runs was fixed before the first one ran. Looked at after every round it
// is a different test, and under no effect at all it crosses 0.05 at some
// round far more often than 5% of the time.
//
// The tests here are mixture sequential probability ratio tests (mSPRT,
// Johari, Pekelis and Walsh, "Always valid inference", 2017). Each keeps a
// likelihood ratio Λn of "some effect, averaged over a prior" against "no
// effect"; under no effect Λn is a nonnegative martingale with mean 1, so
// by Ville's inequality P(Λn ever reaches 1/α) ≤ α. The always-valid
// p-value is 1/max Λ so far, and it may be read after every pair. Both
// gates here are the beta-binomial mixture over a fair coin, which is
// exact: nothing is estimated from the data, so the guarantee holds from
// the first pair.
//
// The unit is a pair: the same task and the same run index in both arms,
// which is the same seed and the same workspace when the runner seeds
// them (effbench does; janetcode's own eval starts every run of a task
// from the same files). A pair where either arm was lost to the broker or
// answered by another vendor's model is counted, per arm, and kept out —
// silently discarding them hides an arm that loses more runs to the
// infrastructure than the other.

// SeqGate names which gate decides when a sequential run may stop.
type SeqGate string

const (
	SeqPass   SeqGate = "pass"   // the pass rate only
	SeqOutput SeqGate = "output" // output tokens only
	SeqAny    SeqGate = "any"    // whichever decides first
)

// ParseSeqGate reads --eval-sequential's value.
func ParseSeqGate(s string) (SeqGate, error) {
	switch g := SeqGate(strings.TrimSpace(s)); g {
	case SeqPass, SeqOutput, SeqAny:
		return g, nil
	}
	return "", fmt.Errorf("unknown sequential gate %q (pass, output or any)", s)
}

// Sequential is the always-valid analysis of a paired A/B.
type Sequential struct {
	// Alpha is the familywise level; each of the two gates is held to
	// Alpha/2.
	Alpha float64
	// Pairs is how many (task, run) pairs both sets have; Used is how many
	// of them both arms scored.
	Pairs, Used int
	// DroppedBase, DroppedCur and DroppedBoth count the pairs left out
	// because one arm, the other, or both were lost to the provider or
	// answered by another vendor's model. Unpaired counts runs that only
	// one set has at all.
	DroppedBase, DroppedCur, DroppedBoth, Unpaired int

	// The pass-rate gate reads only discordant pairs: Wins where current
	// passed and base failed, Losses the other way round. Ties carry no
	// information about which arm is better and do not move the test.
	Wins, Losses, Ties int
	PassP              float64
	// PassStop is the 1-based pair, in run order, at which the pass-rate
	// gate first cleared its level; zero when it never did.
	PassStop int

	// The output-token gate is the same mixture test over which arm wrote
	// fewer output tokens in each pair (Fewer, More; equal counts tie). A
	// sign rather than the size: a normal mixture with the spread
	// estimated from the data crossed its level on 7% of no-effect A/Bs in
	// TestSequentialNoEffectStaysBelowAlpha's simulation, against the 2.5%
	// it promised, and an exact test that is a little slower is the better
	// trade. OutputN pairs had both counts; OutputRatio, the geometric mean
	// of current/base, is the estimate printed beside the verdict.
	OutputN, Fewer, More int
	OutputRatio          float64
	OutputP              float64
	OutputStop           int
}

// Level is what one gate's always-valid p-value has to clear.
func (s Sequential) Level() float64 { return s.Alpha / 2 }

// Decided reports whether the gate g has cleared its level.
func (s Sequential) Decided(g SeqGate) bool {
	pass := s.PassStop > 0
	out := s.OutputStop > 0
	switch g {
	case SeqPass:
		return pass
	case SeqOutput:
		return out
	}
	return pass || out
}

// seqPair is one task-run seen by both sets.
type seqPair struct {
	name      string
	run       int
	base, cur Result
}

// CompareSequential pairs base and cur by task and run index, in run
// order — every task's first run, then every task's second — which is the
// order a round-by-round runner produces them in, and runs both gates over
// that sequence.
func CompareSequential(base, cur []Result, alpha float64) Sequential {
	if alpha <= 0 || alpha >= 1 {
		alpha = 0.05
	}
	s := Sequential{Alpha: alpha, PassP: 1, OutputP: 1}
	type key struct {
		name string
		run  int
	}
	bm := map[key]Result{}
	for _, r := range base {
		bm[key{r.Name, r.Run}] = r
	}
	seen := map[key]bool{}
	var pairs []seqPair
	for _, r := range cur {
		k := key{r.Name, r.Run}
		b, ok := bm[k]
		if !ok {
			s.Unpaired++
			continue
		}
		seen[k] = true
		pairs = append(pairs, seqPair{name: r.Name, run: r.Run, base: b, cur: r})
	}
	for k := range bm {
		if !seen[k] {
			s.Unpaired++
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].run != pairs[j].run {
			return pairs[i].run < pairs[j].run
		}
		return pairs[i].name < pairs[j].name
	})
	s.Pairs = len(pairs)

	var logRatios []float64
	for _, p := range pairs {
		bs, cs := p.base.Scored(), p.cur.Scored()
		switch {
		case !bs && !cs:
			s.DroppedBoth++
			continue
		case !bs:
			s.DroppedBase++
			continue
		case !cs:
			s.DroppedCur++
			continue
		}
		s.Used++
		switch {
		case p.cur.Passed && !p.base.Passed:
			s.Wins++
		case p.base.Passed && !p.cur.Passed:
			s.Losses++
		default:
			s.Ties++
		}
		if pv := 1 / seqBetaLR(s.Wins, s.Losses); pv < s.PassP {
			s.PassP = pv
		}
		if s.PassStop == 0 && s.PassP <= s.Level() {
			s.PassStop = s.Used
		}
		bo, co := p.base.Usage.OutputTokens, p.cur.Usage.OutputTokens
		if bo > 0 && co > 0 {
			logRatios = append(logRatios, math.Log(float64(co)/float64(bo)))
			switch {
			case co < bo:
				s.Fewer++
			case co > bo:
				s.More++
			}
			if pv := 1 / seqBetaLR(s.Fewer, s.More); pv < s.OutputP {
				s.OutputP = pv
			}
			if s.OutputStop == 0 && s.OutputP <= s.Level() {
				s.OutputStop = s.Used
			}
		}
	}
	s.OutputN = len(logRatios)
	if s.OutputN > 0 {
		s.OutputRatio = math.Exp(mean(logRatios))
	}
	s.PassP = math.Min(1, s.PassP)
	s.OutputP = math.Min(1, s.OutputP)
	return s
}

// seqBetaLR is the mixture likelihood ratio for w wins and l losses among
// discordant pairs: under no effect each discordant pair is a fair coin;
// the alternative is a coin of unknown bias, uniform on [0, 1]. The
// marginal likelihood of the sequence under the alternative is
// B(w+1, l+1), under the null 2^-(w+l).
func seqBetaLR(w, l int) float64 {
	lw, _ := math.Lgamma(float64(w + 1))
	ll, _ := math.Lgamma(float64(l + 1))
	ln, _ := math.Lgamma(float64(w + l + 2))
	return math.Exp(lw + ll - ln + float64(w+l)*math.Ln2)
}

// WriteSequential prints the always-valid verdicts and what was dropped.
func WriteSequential(w io.Writer, s Sequential) {
	fmt.Fprintf(w, "\nsequential (mSPRT, always-valid at %.3g per gate): %d paired run(s), %d scored in both arms\n", s.Level(), s.Pairs, s.Used)
	if d := s.DroppedBase + s.DroppedCur + s.DroppedBoth; d > 0 {
		fmt.Fprintf(w, "  dropped pairs: %d lost in base only · %d in current only · %d in both (infrastructure or another vendor's model; counted, not scored)\n", s.DroppedBase, s.DroppedCur, s.DroppedBoth)
	}
	if s.Unpaired > 0 {
		fmt.Fprintf(w, "  %s in one set only\n", textfmt.Plural(s.Unpaired, "run"))
	}
	fmt.Fprintf(w, "  pass rate: %d won · %d lost · %d tied · p=%.3g · %s\n", s.Wins, s.Losses, s.Ties, s.PassP, seqVerdict(s.PassStop, s.Wins > s.Losses, "better", "worse"))
	if s.OutputN > 0 {
		fmt.Fprintf(w, "  output tokens: ×%.2f over %d pair(s), %d fewer · %d more · p=%.3g · %s\n", s.OutputRatio, s.OutputN, s.Fewer, s.More, s.OutputP, seqVerdict(s.OutputStop, s.Fewer > s.More, "fewer", "more"))
	}
}

func seqVerdict(stop int, up bool, yes, no string) string {
	if stop == 0 {
		return "undecided: keep running or call it no detectable effect"
	}
	dir := no
	if up {
		dir = yes
	}
	return fmt.Sprintf("decided (%s) after %d pair(s)", dir, stop)
}
