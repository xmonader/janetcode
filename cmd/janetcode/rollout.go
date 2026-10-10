package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/replay"
	"github.com/xmonader/janetcode/internal/session"
)

// The opt-in second attempt (--branch-retry, --second-sample) runs as a
// child janetcode with this process's own arguments, so the attempt is
// built by exactly the code that built the first one — the same tools,
// prompt, project wiring and permission mode — rather than by a second
// construction path that could drift from it. The child is told what is
// different through a spec file named in headless.RolloutEnv: the prompt
// as sent, and for a branch which recording to replay and how far.
// internal/headless decides when to ask for one and what to keep.

// rolloutSpec is the spec this process was started with as a child, nil
// for an ordinary run.
func rolloutSpec() (*headless.AttemptSpec, error) {
	path := os.Getenv(headless.RolloutEnv)
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", headless.RolloutEnv, err)
	}
	var spec headless.AttemptSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("%s: %w", headless.RolloutEnv, err)
	}
	return &spec, nil
}

// branchFrom points ag at the recording for the first spec.ReplayCalls
// model calls and at its own provider after them: the branch re-executes
// the failed attempt's exploration for free, against the tree the parent
// put back to its start, and goes live from the critical step with the
// note queued for its first live call. Compaction is held off while the
// recording plays — a summary call would consume a recorded reply meant
// for the next step, as --what-if found — and restored at the switch.
func branchFrom(ag *agent.Agent, spec *headless.AttemptSpec) error {
	if spec == nil || spec.Replay == "" || spec.ReplayCalls <= 0 {
		return nil
	}
	recorded, err := replay.Load(spec.Replay)
	if err != nil {
		return fmt.Errorf("branch: load the recording: %w", err)
	}
	// The replayed calls were paid for by the first attempt; counted
	// again here, the run's result object would bill them twice.
	recorded.Free()
	compactAt := ag.CompactAt
	ag.CompactAt = -1
	ag.Provider = replay.UntilCall(recorded, spec.ReplayCalls, ag.Provider, func() {
		// On the turn goroutine, mid-turn: through UpdateSettings, the
		// way anything a running turn reads is changed.
		ag.UpdateSettings(func(s *agent.Settings) { s.CompactAt = compactAt })
		ag.QueueHarnessNote(spec.Note)
	})
	return nil
}

// rolloutAttempt is headless.Rollout's Attempt: it runs this binary again
// with the same arguments and the spec, reads its result object from its
// stdout, and reads its session's events from sessDir. The child's stderr
// is this process's, so its progress shows where the first attempt's did.
func rolloutAttempt(sessDir, cwd string, stderr io.Writer) func(context.Context, headless.AttemptSpec) (headless.Attempt, error) {
	return func(ctx context.Context, spec headless.AttemptSpec) (headless.Attempt, error) {
		exe, err := os.Executable()
		if err != nil {
			exe = os.Args[0]
		}
		f, err := os.CreateTemp("", "janetcode-rollout-*.json")
		if err != nil {
			return headless.Attempt{}, err
		}
		defer os.Remove(f.Name())
		if err := json.NewEncoder(f).Encode(spec); err != nil {
			f.Close()
			return headless.Attempt{}, err
		}
		if err := f.Close(); err != nil {
			return headless.Attempt{}, err
		}
		cmd := exec.CommandContext(ctx, exe, os.Args[1:]...)
		cmd.Dir = cwd
		cmd.Env = append(os.Environ(), headless.RolloutEnv+"="+f.Name())
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr, cmd.Stdin = &out, stderr, nil
		runErr := cmd.Run()
		// A child that failed still printed its result object: the exit
		// status is in it. Only a child with no result at all failed to
		// run.
		res, perr := lastResult(out.Bytes())
		if perr != nil {
			if runErr != nil {
				return headless.Attempt{}, fmt.Errorf("%v (and no result object: %v)", runErr, perr)
			}
			return headless.Attempt{}, perr
		}
		att := headless.Attempt{Result: res}
		if res.SessionID != "" {
			if s, _, err := session.Open(sessDir, res.SessionID); err == nil {
				att.Events = s.Events()
			}
		}
		return att, nil
	}
}

// lastResult is the last result object in a json run's stdout.
func lastResult(out []byte) (headless.CLIResult, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var last *headless.CLIResult
	for {
		var r headless.CLIResult
		if err := dec.Decode(&r); err != nil {
			break
		}
		if r.Type == headless.CLIResultType {
			last = &r
		}
	}
	if last == nil {
		return headless.CLIResult{}, fmt.Errorf("the attempt printed no result object")
	}
	return *last, nil
}
