package mutate

import (
	"context"

	"github.com/xmonader/janetcode/internal/verify"
)

// ForVerify is the verify.MutateFunc that runs mutation testing with a
// Runner; set it as verify.Runner.Mutate.
func ForVerify(ctx context.Context, run verify.MutationRun) (verify.Mutation, error) {
	r := &Runner{Root: run.Root, Command: run.Command, Timeout: run.Timeout, Max: run.Max, Log: run.Log}
	rep, err := r.Run(ctx)
	if err != nil {
		return nil, err
	}
	return rep, nil
}
