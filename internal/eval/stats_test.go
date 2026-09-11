package eval

import (
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

	// A saving that is present on every task is a saving.
	half := []Result{res("a", 0.5), res("b", 0.5), res("c", 0.5), res("d", 2.0)}
	c2 := Compare(base, half)
	if !c2.CostSignificant() {
		t.Fatalf("a consistent halving should survive: ×%.2f [%.2f, %.2f]", c2.CostRatio, c2.CostRatioLo, c2.CostRatioHi)
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
	// Three tasks is enough to bootstrap, and a consistent effect shows.
	three := Compare(
		[]Result{res("a", 1.0), res("b", 1.0), res("c", 1.0)},
		[]Result{res("a", 0.5), res("b", 0.5), res("c", 0.5)})
	if !three.CostSignificant() {
		t.Fatalf("three tasks halving should clear: ×%.2f [%.2f, %.2f]", three.CostRatio, three.CostRatioLo, three.CostRatioHi)
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
