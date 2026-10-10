package eval

import (
	"context"
	"time"
)

// RunRounds is Run one round at a time — every task once, then every task
// again — asking stop after each round whether the evidence so far is
// decisive. It is what lets a sequential A/B (CompareSequential) end early:
// Run queues every repetition at once, so there is no point at which it
// could stop with a clean round behind it. r.Runs is the most rounds run.
// Results carry their round as Run, the index Run would have given them.
func (r *Runner) RunRounds(ctx context.Context, tasks []Task, stop func(done []Result) bool) []Result {
	rounds := max(r.Runs, 1)
	one := *r
	one.Runs = 1
	var all []Result
	for round := range rounds {
		if ctx.Err() != nil {
			break
		}
		start := time.Now()
		res := one.Run(ctx, tasks)
		for i := range res {
			res[i].Run = round
		}
		all = append(all, res...)
		r.logf("round %d/%d done in %s", round+1, rounds, time.Since(start).Round(time.Second))
		if round+1 < rounds && stop != nil && stop(all) {
			r.logf("stopping after round %d of %d: the evidence is decisive", round+1, rounds)
			break
		}
	}
	return all
}
