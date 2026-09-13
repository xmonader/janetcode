package main

import "testing"

// TestEvalVariantNaming checks that --eval-route names a results file's
// variant the same way --eval-off always has, so EVAL_COMPARE reports
// what a step-routing run actually differed by (see docs/manual/evals.md,
// "Measuring step routing").
func TestEvalVariantNaming(t *testing.T) {
	cases := []struct {
		name, label, off, route, want string
	}{
		{"label wins over everything else", "my-experiment", "coach", "haiku", "my-experiment"},
		{"off alone", "", "coach,prune", "", "off:coach,prune"},
		{"route alone", "", "", "claude-haiku-4.5", "route:claude-haiku-4.5"},
		{"off and route together", "", "coach", "claude-haiku-4.5", "off:coach,route:claude-haiku-4.5"},
		{"neither set", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := evalVariant(c.label, c.off, c.route); got != c.want {
				t.Errorf("evalVariant(%q, %q, %q) = %q, want %q", c.label, c.off, c.route, got, c.want)
			}
		})
	}
}
