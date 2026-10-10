package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
)

// rolloutFixture is a git repository with a.py committed, a session whose
// start state is that tree, and the first attempt's edit on disk.
type rolloutFixture struct {
	root string
	sess *session.Session
	rec  *recorder
	errb *bytes.Buffer
	opts Options
}

func newRolloutFixture(t *testing.T) *rolloutFixture {
	t.Helper()
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	write(t, root, "a.py", "start\n")
	git("add", "-A")
	git("commit", "-q", "-m", "init")
	ctx := context.Background()
	repo, err := snapshot.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	start, err := repo.Take(ctx, "start")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SetStart(session.StartState{Head: start.Head, Snap: start.Commit, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	write(t, root, "a.py", "first attempt\n")
	errb := &bytes.Buffer{}
	f := &rolloutFixture{root: root, sess: sess, errb: errb}
	f.opts = Options{Session: sess, Prompt: "fix it", Cwd: root, Stdout: &bytes.Buffer{}, Stderr: errb}
	f.rec = newRecorder(f.opts, FormatJSON)
	return f
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *rolloutFixture) log(t *testing.T, evs ...session.Event) {
	t.Helper()
	for _, e := range evs {
		if _, err := f.sess.Append(e); err != nil {
			t.Fatal(err)
		}
	}
}

func stepEvent() session.Event {
	return session.Event{Type: session.TypeAssistant, Turn: 1, Surface: true, Message: &llm.Message{Role: llm.RoleAssistant}}
}

var callN int

func toolCall(name, args string) session.Event {
	callN++
	return session.Event{Type: session.TypeToolCall, Turn: 1, CallID: fmt.Sprintf("x%d", callN), Name: name, Arguments: args}
}

func verdictEvent(o agent.TurnOutcome) session.Event {
	raw, _ := json.Marshal(o)
	return session.Event{Type: session.TypeVerdict, Turn: 1, Name: string(o.Verdict), Extra: raw}
}

var verified = agent.TurnOutcome{Verdict: agent.VerdictVerified, Edited: 1, Ran: 1, Passed: 1, Claimed: "tests pass"}

// A stuck run that explored first is branched after its exploration: the
// child is told to replay exactly that prefix with a note, it finds the
// tree back at the start, and its clean finish wins and stays on disk.
func TestRolloutBranchAfterBadEnd(t *testing.T) {
	f := newRolloutFixture(t)
	f.log(t, stepEvent(), toolCall("read_file", `{"path":"a.py"}`), stepEvent(), toolCall("edit_file", `{"path":"a.py"}`), stepEvent())
	var got AttemptSpec
	f.opts.Rollout = &Rollout{Branch: true, Attempt: func(ctx context.Context, spec AttemptSpec) (Attempt, error) {
		got = spec
		if s := read(t, f.root, "a.py"); s != "start\n" {
			t.Errorf("the branch began on %q, want the start state", s)
		}
		write(t, f.root, "a.py", "branch\n")
		return Attempt{Result: CLIResult{SessionID: "child", Result: "fixed", Usage: CLIUsage{OutputTokens: 7}, NumTurns: 2},
			Events: []session.Event{verdictEvent(verified)}}, nil
	}}
	err := secondAttempt(context.Background(), f.opts, f.rec, fmt.Errorf("turn: %w", agent.ErrStuck))
	if err != nil {
		t.Fatalf("the branch won, so the run's error should be its (none): %v", err)
	}
	if got.ReplayCalls != 1 || got.Replay != f.sess.Path() || !strings.Contains(got.Note, "stuck") || got.Prompt != "fix it" {
		t.Fatalf("spec = %+v, want 1 replayed call of %s with a note", got, f.sess.Path())
	}
	if s := read(t, f.root, "a.py"); s != "branch\n" {
		t.Fatalf("tree = %q, want the branch's", s)
	}
	res := f.rec.result(nil, err)
	if res.Rollout == nil || res.Rollout.Winner != "branch" || res.Rollout.DecidedBy != "end" || res.Result != "fixed" {
		t.Fatalf("result = %+v / rollout %+v", res, res.Rollout)
	}
	if res.Usage.OutputTokens != 7 || res.NumTurns != 2 {
		t.Fatalf("the branch's usage was not added: %+v, %d calls", res.Usage, res.NumTurns)
	}
}

// A run that edited before it explored anything has no branch point: the
// retry says so and draws nothing, and the first attempt stands.
func TestRolloutBranchWithoutExplorationIsSkipped(t *testing.T) {
	f := newRolloutFixture(t)
	f.log(t, stepEvent(), toolCall("write_file", `{"path":"a.py"}`), stepEvent())
	f.opts.Rollout = &Rollout{Branch: true, Attempt: func(context.Context, AttemptSpec) (Attempt, error) {
		t.Fatal("an attempt was drawn with no branch point")
		return Attempt{}, nil
	}}
	stuck := fmt.Errorf("turn: %w", agent.ErrStuck)
	if err := secondAttempt(context.Background(), f.opts, f.rec, stuck); err != stuck {
		t.Fatalf("err = %v, want the first attempt's", err)
	}
	if res := f.rec.result(nil, stuck); res.Rollout == nil || res.Rollout.Skipped == "" {
		t.Fatalf("rollout = %+v, want a skipped account", res.Rollout)
	}
	if s := read(t, f.root, "a.py"); s != "first attempt\n" {
		t.Fatalf("tree = %q, the first attempt must be untouched", s)
	}
}

// A failure that is not a bad end of the run's making — the budget, a
// dropped connection — fires nothing, and neither does --second-sample
// on a failed run.
func TestRolloutIgnoresOtherFailures(t *testing.T) {
	f := newRolloutFixture(t)
	f.log(t, stepEvent(), toolCall("read_file", `{"path":"a.py"}`), stepEvent(), toolCall("edit_file", `{"path":"a.py"}`), stepEvent())
	f.opts.Rollout = &Rollout{Branch: true, Sample: true, Attempt: func(context.Context, AttemptSpec) (Attempt, error) {
		t.Fatal("an attempt was drawn")
		return Attempt{}, nil
	}}
	for _, err := range []error{agent.ErrBudget, errors.New("connection reset")} {
		if got := secondAttempt(context.Background(), f.opts, f.rec, err); got != err {
			t.Fatalf("err = %v, want %v", got, err)
		}
	}
	if f.rec.rollout != nil {
		t.Fatalf("rollout recorded: %+v", f.rec.rollout)
	}
}

// A green run whose verdict stands draws no second sample: that is the
// common case, and it must cost nothing.
func TestRolloutSampleNotOnStrongEvidence(t *testing.T) {
	f := newRolloutFixture(t)
	f.log(t, stepEvent(), toolCall("edit_file", `{"path":"a.py"}`), verdictEvent(verified))
	f.opts.Rollout = &Rollout{Sample: true, Attempt: func(context.Context, AttemptSpec) (Attempt, error) {
		t.Fatal("a second sample was drawn on a verified run")
		return Attempt{}, nil
	}}
	if err := secondAttempt(context.Background(), f.opts, f.rec, nil); err != nil || f.rec.rollout != nil {
		t.Fatalf("err %v, rollout %+v; want nothing", err, f.rec.rollout)
	}
}

// A green run on weak evidence draws a fresh sample (no replay, no note);
// when the sample ends worse, the first attempt's tree comes back and the
// run's answer and exit stand.
func TestRolloutSampleLosesAndFirstTreeComesBack(t *testing.T) {
	f := newRolloutFixture(t)
	over := agent.TurnOutcome{Verdict: agent.VerdictUnverified, Edited: 1, Claimed: "tests pass"}
	f.log(t, stepEvent(), toolCall("edit_file", `{"path":"a.py"}`), verdictEvent(over))
	var got AttemptSpec
	f.opts.Rollout = &Rollout{Sample: true, Attempt: func(ctx context.Context, spec AttemptSpec) (Attempt, error) {
		got = spec
		write(t, f.root, "a.py", "sample\n")
		return Attempt{Result: CLIResult{IsError: true, Error: agent.ErrStuck.Error(), ExitCode: 1}}, nil
	}}
	if err := secondAttempt(context.Background(), f.opts, f.rec, nil); err != nil {
		t.Fatalf("err = %v, want the first attempt's (none)", err)
	}
	if got.Replay != "" || got.ReplayCalls != 0 || got.Note != "" {
		t.Fatalf("a second sample is an independent draw, got %+v", got)
	}
	if s := read(t, f.root, "a.py"); s != "first attempt\n" {
		t.Fatalf("tree = %q, want the first attempt's back", s)
	}
	if f.rec.rollout == nil || f.rec.rollout.Winner != "first" {
		t.Fatalf("rollout = %+v", f.rec.rollout)
	}
}

// When the extra attempt's tree is kept and it failed, its failure and
// exit code are the run's.
func TestRolloutWinnerErrorCarriesItsExitCode(t *testing.T) {
	f := newRolloutFixture(t)
	f.log(t, stepEvent(), toolCall("read_file", `{"path":"a.py"}`), stepEvent(), toolCall("edit_file", `{"path":"a.py"}`), stepEvent())
	f.opts.Rollout = &Rollout{Branch: true, Attempt: func(context.Context, AttemptSpec) (Attempt, error) {
		// Ended on the budget: not a bad end, so it outranks a stuck
		// first attempt, and still failed.
		return Attempt{Result: CLIResult{IsError: true, Error: "the run reached its cost budget", ExitCode: ExitBudget}}, nil
	}}
	err := secondAttempt(context.Background(), f.opts, f.rec, fmt.Errorf("turn: %w", agent.ErrStuck))
	if err == nil || ExitCode(err) != ExitBudget {
		t.Fatalf("err = %v (exit %d), want the branch's failure and exit %d", err, ExitCode(err), ExitBudget)
	}
}
