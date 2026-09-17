package eval

import (
	"math"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestRatioIntervalCatchesNoise is the check on the check: a difference
// that comes from one task swinging must not be reported as a saving.
// This is the mistake the harness let through before — a point-estimate
// ratio computed from totals, quoted as a result — so the test is written
// against the shape of that mistake.
func TestRatioIntervalCatchesNoise(t *testing.T) {
	// Four tasks. Three cost the same in both sets; one is much cheaper in
	// current. The totals move by a lot, but a paired bootstrap over four
	// tasks cannot tell that apart from noise.
	base := []Result{
		res("a", 1.0), res("b", 1.0), res("c", 1.0), res("d", 4.0),
	}
	cur := []Result{
		res("a", 1.0), res("b", 1.0), res("c", 1.0), res("d", 1.0),
	}
	c := Compare(base, cur)
	if !c.Paired {
		t.Fatal("four shared tasks should pair")
	}
	if c.CostRatio >= 1 {
		t.Fatalf("totals did fall: ratio %.2f", c.CostRatio)
	}
	if c.CostSignificant() {
		t.Fatalf("one task swinging is not a saving: ×%.2f [%.2f, %.2f]", c.CostRatio, c.CostRatioLo, c.CostRatioHi)
	}
	var b strings.Builder
	WriteComparison(&b, c)
	if !strings.Contains(b.String(), "inside the noise") {
		t.Fatalf("the report must say so:\n%s", b.String())
	}

	// A saving present on every task is a saving — once there are enough
	// tasks for "every one of them" to mean something. Four all moving
	// the same way is p=0.125 by the sign test, before the correction for
	// testing four gates at once; eight is p≈0.008, which clears it.
	var wide, halved []Result
	for i := range 8 {
		name := string(rune('a' + i))
		wide = append(wide, res(name, 1.0))
		halved = append(halved, res(name, 0.5))
	}
	c2 := Compare(wide, halved)
	if !c2.CostSignificant() {
		t.Fatalf("a consistent halving over eight tasks should survive: ×%.2f [%.2f, %.2f] p=%.4g", c2.CostRatio, c2.CostRatioLo, c2.CostRatioHi, c2.CostP)
	}
	var b2 strings.Builder
	WriteComparison(&b2, c2)
	if !strings.Contains(b2.String(), "real saving") {
		t.Fatalf("the report must say so:\n%s", b2.String())
	}
}

func res(name string, cost float64) Result {
	return Result{
		Name: name, Passed: true, CostUSD: cost, CostKnown: true,
		Usage: llm.Usage{InputTokens: int(cost * 1000), OutputTokens: 10},
	}
}

// TestOneTaskCannotBeSignificant: a bootstrap resamples tasks, so with one
// task every resample is the same value and the interval collapses to a
// point that reads as certainty. The harness produced exactly that —
// "×1.13 [1.13, 1.13] · real increase" — on a single-task comparison.
func TestOneTaskCannotBeSignificant(t *testing.T) {
	one := Compare([]Result{res("a", 1.0)}, []Result{res("a", 1.13)})
	if one.CostSignificant() || one.ContextSignificant() {
		t.Fatalf("one task cannot be significant: cost ×%.2f [%.2f, %.2f]", one.CostRatio, one.CostRatioLo, one.CostRatioHi)
	}
	if one.CostRatio == 0 {
		t.Fatal("the ratio itself is still worth reporting")
	}
	var b strings.Builder
	WriteComparison(&b, one)
	if !strings.Contains(b.String(), "too few tasks") {
		t.Fatalf("the report must say why there is no interval:\n%s", b.String())
	}
	if strings.Contains(b.String(), "real increase") || strings.Contains(b.String(), "real saving") {
		t.Fatalf("no verdict may be claimed:\n%s", b.String())
	}
	// Three tasks is enough to run a bootstrap, but not enough to claim
	// anything from one. Three identical halvings resample to ×0.50
	// [0.50, 0.50] — a point printed as an interval, which reads as
	// certainty about a result a sign test puts at p=0.25. The n>=3 gate
	// did not prevent that; it only moved the task count at which it
	// happened.
	three := Compare(
		[]Result{res("a", 1.0), res("b", 1.0), res("c", 1.0)},
		[]Result{res("a", 0.5), res("b", 0.5), res("c", 0.5)})
	if three.CostSignificant() {
		t.Fatalf("three identical halvings are p=0.25, not a verdict: ×%.2f p=%.4g", three.CostRatio, three.CostP)
	}
	if three.CostInterval {
		t.Fatalf("a collapsed bootstrap must not be reported as an interval: [%.2f, %.2f]", three.CostRatioLo, three.CostRatioHi)
	}
	if three.CostRatio == 0 {
		t.Fatal("the ratio itself is still worth reporting")
	}
}

// TestFreshTokensAreReportedApart: a cache read costs about a tenth of a
// fresh input token, so a harness taking more turns re-reads the same
// cached prefix more often and "context" rises far faster than the bill.
// A 48% cost increase once showed up as 106% here and was chased as a
// regression on that basis, so the two are now shown apart.
func TestFreshTokensAreReportedApart(t *testing.T) {
	cheap := llm.Usage{InputTokens: 100, CacheReadTokens: 9_900}
	dear := llm.Usage{InputTokens: 10_000}
	if contextTokens(cheap) != contextTokens(dear) {
		t.Fatal("both read the same amount of conversation")
	}
	if freshTokens(cheap) >= freshTokens(dear) {
		t.Fatalf("one of them paid full price for all of it: %d vs %d", freshTokens(cheap), freshTokens(dear))
	}
	var b strings.Builder
	WriteComparison(&b, Compare(
		[]Result{{Name: "a", Passed: true, Usage: cheap, CostKnown: true, CostUSD: 1}},
		[]Result{{Name: "a", Passed: true, Usage: dear, CostKnown: true, CostUSD: 1}}))
	if !strings.Contains(b.String(), "fresh") {
		t.Fatalf("the report must separate them:\n%s", b.String())
	}
}

// A run that passed because the harness rescued it three times is not the
// same run as one that passed cleanly, and the report said only how many
// passed. The prose recoveries were carried per run and the other two
// kinds were not, and nothing printed any of them.
func TestHarnessRepairsAreSummarisedAndPrinted(t *testing.T) {
	s := Summarize([]Result{
		{Name: "a", Passed: true, Recoveries: 2, WatchdogKills: 1},
		{Name: "b", Passed: true, ModelFallbacks: 3},
	})
	if s.Recoveries != 2 || s.WatchdogKills != 1 || s.ModelFallbacks != 3 {
		t.Fatalf("summary = %d/%d/%d", s.Recoveries, s.WatchdogKills, s.ModelFallbacks)
	}
	line := summaryLine(s)
	if !strings.Contains(line, "harness repairs: 6") {
		t.Fatalf("the summary does not say what the harness had to rescue:\n%s", line)
	}
	if clean := summaryLine(Summarize([]Result{{Name: "a", Passed: true}})); strings.Contains(clean, "harness repairs") {
		t.Fatalf("a clean set should say nothing:\n%s", clean)
	}
}

// The paired bootstrap is seeded so a report is reproducible. The seed
// fixes which *indices* each resample draws, so it only buys
// reproducibility if the pairs behind those indices are in a fixed order
// too — and they were built by ranging over a map, whose order Go
// randomises on every run. Comparing the same two result sets therefore
// printed a different interval each time.
func TestPairedBootstrapIsReproducible(t *testing.T) {
	base := []Result{res("a", 1.0), res("b", 2.0), res("c", 3.0), res("d", 4.0), res("e", 5.0), res("f", 6.0)}
	cur := []Result{res("a", 1.1), res("b", 1.5), res("c", 4.0), res("d", 3.0), res("e", 9.0), res("f", 2.0)}
	want := Compare(base, cur)
	if want.CostRatioLo == 0 && want.CostRatioHi == 0 {
		t.Fatal("six tasks should bootstrap an interval at all")
	}
	for i := 0; i < 20; i++ {
		got := Compare(base, cur)
		if got.CostRatioLo != want.CostRatioLo || got.CostRatioHi != want.CostRatioHi {
			t.Fatalf("cost interval moved between identical comparisons: [%v, %v] then [%v, %v]",
				want.CostRatioLo, want.CostRatioHi, got.CostRatioLo, got.CostRatioHi)
		}
		if got.ContextRatioLo != want.ContextRatioLo || got.ContextRatioHi != want.ContextRatioHi {
			t.Fatal("context interval moved between identical comparisons")
		}
		if got.OutputRatioLo != want.OutputRatioLo || got.OutputRatioHi != want.OutputRatioHi {
			t.Fatal("output interval moved between identical comparisons")
		}
	}
}

// TestOneTaskPassRateCannotBeSignificant: the pass-rate delta shared the
// collapse the ratios had — with one shared task the bootstrap interval
// is [delta, delta], and any nonzero delta printed as "significant
// improvement". The guard mirrors ratioCI's minPairs.
func TestOneTaskPassRateCannotBeSignificant(t *testing.T) {
	fail := res("a", 1.0)
	fail.Passed = false
	pass := res("a", 1.0)
	one := Compare([]Result{fail}, []Result{pass})
	if one.Significant() {
		t.Fatalf("one task cannot be significant: delta %+.1f%% [%+.1f, %+.1f]", 100*one.Delta, 100*one.Lo, 100*one.Hi)
	}
	var b strings.Builder
	WriteComparison(&b, one)
	if !strings.Contains(b.String(), "too few shared tasks") {
		t.Fatalf("the report must say why there is no interval:\n%s", b.String())
	}
	if strings.Contains(b.String(), "significant improvement") {
		t.Fatalf("no verdict may be claimed:\n%s", b.String())
	}
	mk := func(name string, passed bool) Result {
		r := res(name, 1.0)
		r.Passed = passed
		return r
	}
	// Three shared tasks bootstrap again — to [+100%, +100%], a point
	// that reads as certainty about something a sign test puts at
	// p=0.25. Reporting that as a "significant improvement" was the
	// overclaim this file exists to prevent.
	three := Compare(
		[]Result{mk("a", false), mk("b", false), mk("c", false)},
		[]Result{mk("a", true), mk("b", true), mk("c", true)})
	if three.Significant() {
		t.Fatalf("three tasks flipping is p=0.25, not a verdict: %+.1f%% p=%.4g", 100*three.Delta, three.DeltaP)
	}
	if three.DeltaInterval {
		t.Fatalf("a collapsed bootstrap must not be reported as an interval: [%+.1f, %+.1f]", 100*three.Lo, 100*three.Hi)
	}
	// Enough tasks all moving the same way is evidence, and the sign test
	// is what says how much: eight is p≈0.008.
	var before, after []Result
	for i := range 8 {
		name := string(rune('a' + i))
		before = append(before, mk(name, false))
		after = append(after, mk(name, true))
	}
	eight := Compare(before, after)
	if !eight.Significant() {
		t.Fatalf("eight tasks flipping should clear: %+.1f%% p=%.4g", 100*eight.Delta, eight.DeltaP)
	}
}

// TestMultipleComparisonsAreCorrectedNotJustDisclosed: one comparison
// tests four gates at once — pass rate, cost, context, output — and the
// report only said so, in a sentence counting the verdicts that *fired*.
// The quiet gates were tested too, so counting the loud ones corrects
// nothing, and the common single-verdict report printed no disclosure at
// all while carrying the same familywise risk.
func TestMultipleComparisonsAreCorrectedNotJustDisclosed(t *testing.T) {
	var base, cur []Result
	for i := range 6 {
		name := string(rune('a' + i))
		base = append(base, res(name, 1.0))
		cur = append(cur, res(name, 0.9))
	}
	c := Compare(base, cur)
	if got := c.Gates(); got != 4 {
		t.Fatalf("Gates() = %d, want the four gates that were tested", got)
	}
	if c.level() >= alpha {
		t.Fatalf("level() = %g, want it tightened below %g", c.level(), alpha)
	}
	var b strings.Builder
	WriteComparison(&b, c)
	out := b.String()
	if !strings.Contains(out, "gates were tested together") || !strings.Contains(out, "had to clear") {
		t.Fatalf("the report must state the correction it applied:\n%s", out)
	}
}

// TestNoPhantomVerdictCount: with too few shared tasks to bootstrap, the
// disclosure counted ratios whose bounds were still zero — "0 < 1" — so a
// report that drew no verdict at all announced that three had been drawn.
func TestNoPhantomVerdictCount(t *testing.T) {
	two := Compare(
		[]Result{res("a", 1.0), res("b", 1.0)},
		[]Result{res("a", 0.5), res("b", 0.5)})
	if two.CostSignificant() || two.ContextSignificant() || two.OutputSignificant() {
		t.Fatal("two shared tasks cannot draw a verdict")
	}
	var b strings.Builder
	WriteComparison(&b, two)
	out := b.String()
	if !strings.Contains(out, "too few tasks to bootstrap an interval") {
		t.Fatalf("the report must say there is no interval:\n%s", out)
	}
	if strings.Contains(out, "gates were tested together") {
		t.Fatalf("a report that drew nothing must not announce a correction:\n%s", out)
	}
}

// TestSignTestP is the exact two-sided binomial the collapsed bootstrap
// falls back to: it is the whole difference between "three tasks agreed"
// and "twenty tasks agreed", which the bootstrap cannot see.
func TestSignTestP(t *testing.T) {
	for _, tc := range []struct {
		vals []float64
		want float64
	}{
		{[]float64{1, 1, 1}, 0.25},
		{[]float64{1, 1, 1, 1}, 0.125},
		{[]float64{1, 1, 1, 1, 1, 1}, 0.03125},
		{[]float64{-1, -1, -1, -1, -1, -1}, 0.03125},
		{[]float64{1, -1}, 1},
		{[]float64{0, 0, 0}, 1}, // all ties
	} {
		if got := signTestP(tc.vals, 0); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("signTestP(%v) = %g, want %g", tc.vals, got, tc.want)
		}
	}
}
