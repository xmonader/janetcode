package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/xmonader/janetcode/internal/eval"
)

// seqRun runs the eval: every round at once, as always, unless
// --eval-sequential asked for rounds one at a time against the baseline,
// stopping at the first round after which the always-valid test on the
// chosen gate is decisive (see internal/eval/sequential.go). Opt-in: it
// changes how many runs are bought, and so what a results file holds.
func seqRun(ctx context.Context, r *eval.Runner, tasks []eval.Task, baseline *eval.File, gate string) ([]eval.Result, error) {
	if gate == "" {
		return r.Run(ctx, tasks), nil
	}
	g, err := eval.ParseSeqGate(gate)
	if err != nil {
		return nil, fmt.Errorf("--eval-sequential: %w", err)
	}
	if baseline == nil {
		return nil, errors.New("--eval-sequential needs --eval-compare: the test pairs each run with the baseline's same task and run")
	}
	if baseline.Runs < r.Runs {
		fmt.Fprintf(os.Stderr, "note: the baseline has %d run(s) per task; rounds past that have nothing to pair with and cannot move the test\n", baseline.Runs)
	}
	return r.RunRounds(ctx, tasks, func(done []eval.Result) bool {
		s := eval.CompareSequential(baseline.Results, done, 0.05)
		fmt.Fprintf(os.Stderr, "sequential: %d scored pair(s) · pass p=%.3g · output p=%.3g (level %.3g)\n", s.Used, s.PassP, s.OutputP, s.Level())
		return s.Decided(g)
	}), nil
}
