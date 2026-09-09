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
