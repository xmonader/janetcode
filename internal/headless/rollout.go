package headless

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/rollout"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
)

// Rollout is the opt-in second attempt at a headless prompt (see
// internal/rollout): the checkpoint-branch retry after a bad end, and the
// verdict-gated second sample after a green run on weak evidence. At most
// one extra attempt is drawn per run, whichever fires.
//
// Both are opt-in because both buy model calls: a fired trigger can
// double the run's cost, and neither has a live result behind it yet.
type Rollout struct {
	// Branch is --branch-retry.
	Branch bool
	// Sample is --second-sample.
	Sample bool
	// Attempt runs one more attempt at the same prompt in the same
	// directory, as a fresh janetcode process with the same flags (so it
	// is built exactly as the first attempt was), and returns its result
	// object and its session's events. cmd/janetcode supplies it.
	Attempt func(ctx context.Context, spec AttemptSpec) (Attempt, error)
}

// RolloutEnv names the environment variable that carries an AttemptSpec's
// file to the child process: an internal handshake, not a user setting.
const RolloutEnv = "JANETCODE_ROLLOUT_ATTEMPT"

// AttemptSpec is what the child process is told.
type AttemptSpec struct {
	// Prompt is the first attempt's prompt as sent, mentions expanded and
	// piped stdin already folded in, so the child sends the same bytes.
	Prompt string `json:"prompt"`
	// Replay, for a branch, is the first attempt's session file, and
	// ReplayCalls how many of its recorded model calls are replayed
	// before the child goes live (replay.UntilCall); Note is the line the
	// live part is told. All three are empty for a second sample, which
	// is an independent draw from the start.
	Replay      string `json:"replay,omitempty"`
	ReplayCalls int    `json:"replay_calls,omitempty"`
	Note        string `json:"note,omitempty"`
	// MaxCostUSD is what is left of --max-cost for the child; zero is no
	// ceiling.
	MaxCostUSD float64 `json:"max_cost_usd,omitempty"`
}

// Attempt is what one extra attempt came back with.
type Attempt struct {
	Result CLIResult
	Events []session.Event
}

// CLIRollout is the result object's account of a rollout: what fired,
// what was drawn, and which tree was kept.
type CLIRollout struct {
	// Mode is "branch" or "sample".
	Mode string `json:"mode"`
	// Trigger is why it fired: the bad end, or the weak-evidence reasons.
	Trigger []string `json:"trigger"`
	// Critical is the branch point, for a branch.
	Critical *rollout.Critical `json:"critical,omitempty"`
	// Skipped says why no attempt was drawn although the trigger fired
	// (no branch point, no recorded start state, the budget spent, ...).
	Skipped string `json:"skipped,omitempty"`
	// Candidates are both attempts' ranked facts, first attempt first.
	Candidates []rollout.Candidate `json:"candidates,omitempty"`
	// Winner names the attempt whose tree was kept, and DecidedBy the
	// ranking key that decided it ("" for a tie, which the first wins).
	Winner    string `json:"winner,omitempty"`
	DecidedBy string `json:"decided_by,omitempty"`
	// SessionID is the extra attempt's own session.
	SessionID string `json:"session_id,omitempty"`
	// Usage and CostUSD are the extra attempt's alone; the result's own
	// usage and cost already include them.
	Usage   CLIUsage `json:"usage"`
	CostUSD float64  `json:"cost_usd"`
}

// attemptError is the extra attempt's failure standing as the run's,
// when its tree was kept: its message and exit code are the child's.
type attemptError struct {
	msg  string
	code int
}

func (e attemptError) Error() string { return e.msg }

// secondAttempt draws the one extra attempt Rollout allows when a trigger
// fires, and returns the run's error as it now stands. The attempts run
// one after the other in the same tree, never side by side: the first
// attempt's tree is snapshotted, the tree is put back to the session's
// recorded start state, the second attempt runs, its tree is snapshotted,
// and the losing one is never left on disk. Running in place rather than
// in a scratch worktree is deliberate: the child keeps the first
// attempt's working directory, so its prompt (which names the directory)
// stays byte-identical and the branch's prefix cache-warm, and the
// ignored files a snapshot does not carry (a virtualenv, node_modules)
// are still there for its tests.
func secondAttempt(ctx context.Context, opts Options, rec *recorder, runErr error) error {
	ro := opts.Rollout
	if ro == nil || ro.Attempt == nil || opts.Session == nil || ctx.Err() != nil {
		return runErr
	}
	events := opts.Session.Events()
	info := CLIRollout{}
	spec := AttemptSpec{Prompt: opts.Prompt}
	end := rollout.BadEnd(runErr, events)
	if errors.Is(runErr, ErrUntilFailed) {
		end = rollout.EndUntilRed
	}
	switch {
	case ro.Branch && end != rollout.EndClean:
		c := rollout.CriticalStep(events)
		info.Mode, info.Trigger, info.Critical = "branch", []string{string(end)}, &c
		if !c.OK() {
			info.Skipped = "no branch point: the run did not explore before it first edited, or explored to its last call"
			break
		}
		spec.Replay, spec.ReplayCalls = opts.Session.Path(), c.Step
		spec.Note = rollout.BranchNote(events, c, end, opts.Cwd)
	case ro.Sample && runErr == nil && !rollout.GateRed(events):
		why := rollout.Weak(events)
		if len(why) == 0 {
			return runErr
		}
		info.Mode, info.Trigger = "sample", why
	default:
		return runErr
	}
	if info.Skipped == "" && opts.MaxCostUSD > 0 {
		spent, ok := rec.spentUSD()
		if ok && spent >= opts.MaxCostUSD {
			info.Skipped = "the --max-cost budget is spent"
		}
		spec.MaxCostUSD = opts.MaxCostUSD - spent
	}
	start := opts.Session.Meta.Start
	if info.Skipped == "" && (start == nil || start.Snap == "") {
		info.Skipped = "the session has no recorded start state to put the tree back to (not a git repository?)"
	}
	if info.Skipped != "" {
		rec.setRollout(info, nil, false)
		fmt.Fprintf(opts.Stderr, "[rollout] %s fired (%s) but no attempt was drawn: %s\n", info.Mode, strings.Join(info.Trigger, "; "), info.Skipped)
		return runErr
	}

	repo, err := snapshot.Open(ctx, opts.Cwd)
	if err != nil {
		return skipRollout(opts, rec, info, runErr, "the directory is not a git repository")
	}
	first, err := repo.Take(ctx, "rollout: first attempt of "+opts.Session.Meta.ID)
	if err != nil {
		return skipRollout(opts, rec, info, runErr, "could not snapshot the first attempt's tree: "+err.Error())
	}
	if _, err := repo.Restore(ctx, snapshot.Snap{Commit: start.Snap, Head: start.Head}); err != nil {
		// A partial restore is undone before anything else happens: the
		// first attempt's tree is the one the user has.
		_, _ = repo.Restore(context.WithoutCancel(ctx), first)
		return skipRollout(opts, rec, info, runErr, "could not put the tree back to its start: "+err.Error())
	}
	fmt.Fprintf(opts.Stderr, "[rollout] %s: %s · first attempt kept as snapshot %s\n", info.Mode, rolloutLine(info), shortSnap(first.Commit))
	att, err := ro.Attempt(ctx, spec)
	if err != nil {
		if _, rerr := repo.Restore(context.WithoutCancel(ctx), first); rerr != nil {
			fmt.Fprintf(opts.Stderr, "[rollout] could not put the first attempt's tree back (%v); it is snapshot %s\n", rerr, first.Commit)
		}
		return skipRollout(opts, rec, info, runErr, "the extra attempt did not run: "+err.Error())
	}
	cands := []rollout.Candidate{
		rollout.NewCandidate("first", events, runErr),
		rollout.NewCandidateText(info.Mode, att.Events, resultErr(att.Result)),
	}
	rank := rollout.Rank(cands, nil)
	info.Candidates, info.DecidedBy, info.Winner = cands, rank.By, cands[rank.Winner].Name
	info.SessionID = att.Result.SessionID
	info.Usage, info.CostUSD = att.Result.Usage, att.Result.CostUSD
	if rank.Winner == 0 {
		// The second attempt's tree goes, the first's comes back; the
		// second's is snapshotted first so nothing is lost either way.
		second, serr := repo.Take(context.WithoutCancel(ctx), "rollout: "+info.Mode+" attempt of "+opts.Session.Meta.ID)
		if _, rerr := repo.Restore(context.WithoutCancel(ctx), first); rerr != nil {
			fmt.Fprintf(opts.Stderr, "[rollout] could not put the first attempt's tree back (%v); it is snapshot %s\n", rerr, first.Commit)
		} else if serr == nil {
			fmt.Fprintf(opts.Stderr, "[rollout] kept the first attempt (%s); the %s attempt is snapshot %s\n", decided(rank), info.Mode, shortSnap(second.Commit))
		}
	} else {
		fmt.Fprintf(opts.Stderr, "[rollout] kept the %s attempt (%s); the first attempt is snapshot %s\n", info.Mode, decided(rank), shortSnap(first.Commit))
	}
	rec.setRollout(info, &att.Result, rank.Winner == 1)
	if rank.Winner == 1 {
		if rec.text() && att.Result.Result != "" {
			fmt.Fprintf(opts.Stdout, "\n%s\n", att.Result.Result)
		}
		if att.Result.IsError {
			return attemptError{msg: resultErr(att.Result), code: att.Result.ExitCode}
		}
		return nil
	}
	return runErr
}

func skipRollout(opts Options, rec *recorder, info CLIRollout, runErr error, why string) error {
	info.Skipped = why
	rec.setRollout(info, nil, false)
	fmt.Fprintf(opts.Stderr, "[rollout] %s fired but no attempt was drawn: %s\n", info.Mode, why)
	return runErr
}

func rolloutLine(info CLIRollout) string {
	if info.Mode == "branch" && info.Critical != nil {
		c := info.Critical
		return fmt.Sprintf("the run ended %s; branching after call %d of %d, replayed from the recording", info.Trigger[0], c.Step, c.Calls)
	}
	return "green on weak evidence (" + strings.Join(info.Trigger, "; ") + "); drawing a second sample from the start"
}

func decided(r rollout.Ranking) string {
	if r.By == "" {
		return "a tie, which the first attempt wins"
	}
	return "decided by " + r.Why
}

func shortSnap(c string) string {
	if len(c) > 10 {
		return c[:10]
	}
	return c
}

func resultErr(r CLIResult) string {
	if !r.IsError {
		return ""
	}
	if r.Error == "" {
		return "the attempt failed"
	}
	return r.Error
}

// setRollout records the rollout for the result object. att is the extra
// attempt's own result, when one ran; won says its tree was kept, so its
// answer and verdict stand as the run's.
func (r *recorder) setRollout(info CLIRollout, att *CLIResult, won bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollout = &info
	r.attempt = att
	r.attemptWon = won
}

// mergeAttempt folds the extra attempt into the result object: its usage,
// cost and calls are added (the run bought them), and when its tree was
// kept its answer and verdict replace the first attempt's.
func (r *recorder) mergeAttempt(res *CLIResult) {
	if r.rollout == nil {
		return
	}
	res.Rollout = r.rollout
	a := r.attempt
	if a == nil {
		return
	}
	res.Usage = CLIUsage{
		InputTokens:      res.Usage.InputTokens + a.Usage.InputTokens,
		OutputTokens:     res.Usage.OutputTokens + a.Usage.OutputTokens,
		CacheReadTokens:  res.Usage.CacheReadTokens + a.Usage.CacheReadTokens,
		CacheWriteTokens: res.Usage.CacheWriteTokens + a.Usage.CacheWriteTokens,
	}
	res.CostUSD += a.CostUSD
	res.NumTurns += a.NumTurns
	res.ToolCalls += a.ToolCalls
	res.PermissionDenials += a.PermissionDenials
	res.Retries += a.Retries
	if r.attemptWon {
		res.Result, res.Verdict = a.Result, a.Verdict
	}
}
