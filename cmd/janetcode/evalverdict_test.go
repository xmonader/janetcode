package main

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/eval"
)

// An eval whose every run was lost to the gateway has not passed; one
// whose runs another vendor answered passes or fails on the rest, unless
// strict provenance makes the swap itself the failure.
func TestEvalVerdict(t *testing.T) {
	for _, c := range []struct {
		s      eval.Summary
		runs   int
		passed int
		strict bool
		want   string
	}{
		{eval.Summary{Total: 2, Passed: 2}, 2, 2, false, ""},
		{eval.Summary{Total: 2, Passed: 1}, 2, 1, false, "1 of 2 scored"},
		{eval.Summary{Total: 0, Infra: 3}, 3, 0, false, "none of 3 eval runs reached"},
		{eval.Summary{Total: 1, Passed: 1, Mismatched: 2}, 3, 1, false, ""},
		{eval.Summary{Total: 1, Passed: 1, Mismatched: 2}, 3, 1, true, "another vendor's model"},
	} {
		err := evalVerdict(c.s, c.runs, c.passed, c.strict)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("evalVerdict(%+v, strict=%v) = %q, want %q", c.s, c.strict, got, c.want)
		}
	}
}
