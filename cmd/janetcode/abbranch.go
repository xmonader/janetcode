package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/replay"
)

// abBranch is --ab-replay: an A/B arm that branches mid-run. The first
// calls model replies come from a recorded run of the same task, served
// at no cost and re-executed against this run's own workspace, and the
// run goes live from the next call. Both arms of an A/B given the same
// recording and the same calls share an identical history up to there, so
// the variance the first replies cause is gone from the comparison; it
// suits a harness change that only acts late in a run (a gate, the spec
// review, compaction). It is branchFrom without a note, and with the
// recording's absolute paths moved from the directory it ran in to this
// one (replay.Rebase), since a benchmark arm runs in a fresh workspace.
//
// Opt-in, being a flag: it replaces the model for the first calls.
func abBranch(ag *agent.Agent, path string, calls int, cwd string, rollout bool) error {
	if path == "" {
		return nil
	}
	if rollout {
		return errors.New("--ab-replay cannot be combined with --branch-retry or --second-sample: the extra attempt would replay the recording a second time")
	}
	if calls <= 0 {
		return errors.New("--ab-replay needs --ab-replay-calls N, the number of recorded model calls to replay before going live")
	}
	recorded, err := replay.Load(path)
	if err != nil {
		return fmt.Errorf("--ab-replay: %w", err)
	}
	if calls >= recorded.Calls() {
		return fmt.Errorf("--ab-replay-calls %d: the recording holds %d model call(s), so nothing would run live", calls, recorded.Calls())
	}
	from, err := replay.RecordedCwd(path)
	if err != nil {
		return fmt.Errorf("--ab-replay: %w", err)
	}
	moved := recorded.Rebase(from, cwd)
	// What the recording cost was paid when it ran; this arm reports what
	// it bought.
	recorded.Free()
	compactAt := ag.CompactAt
	ag.CompactAt = -1
	ag.Provider = replay.UntilCall(recorded, calls, ag.Provider, func() {
		ag.UpdateSettings(func(s *agent.Settings) { s.CompactAt = compactAt })
	})
	fmt.Fprintf(os.Stderr, "[ab-replay] replaying %d of %d recorded model calls, then live; %d block(s) moved from %s\n", calls, recorded.Calls(), moved, from)
	return nil
}
