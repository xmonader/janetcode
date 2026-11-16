package eval

import (
	"testing"

	"github.com/xmonader/janetcode/internal/evidence"
)

// A hidden eval check reads on the evidence model: its exit code decides
// passed or failed, and a run that never reached the model or answered
// from another model is not evidence about the model asked for.
func TestResultEvidenceStatus(t *testing.T) {
	cases := []struct {
		name string
		r    Result
		want evidence.Status
	}{
		{"hidden check passed", Result{Passed: true}, evidence.Passed},
		{"hidden check failed", Result{}, evidence.Failed},
		{"never reached the model", Result{Infra: true}, evidence.Environmental},
		{"another model answered", Result{Mismatches: 1}, evidence.Inconclusive},
		{"polluted workspace", Result{Passed: true, Polluted: []string{"tests/hidden_test.go"}}, evidence.Inconclusive},
	}
	for _, c := range cases {
		if got := c.r.Evidence(); got != c.want {
			t.Errorf("%s: %s, want %s (outcome %s)", c.name, got, c.want, c.r.Outcome())
		}
	}
}
