package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/replay"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// modelCalls is the names of the tool calls the model asked for itself, in
// order: calls a script, run_code or recipe made on its behalf
// (session.IsNestedCall) are left out on both sides of a comparison, the
// same way --what-if leaves them out.
func modelCalls(events []session.Event) []string {
	var out []string
	for _, e := range events {
		if e.Type == session.TypeToolCall && !session.IsNestedCall(e.CallID) {
			out = append(out, e.Name)
		}
	}
	return out
}

// replaySession re-runs the recording at path with the model's part read
// from disk and returns the recorded event log and the replay's own.
// Turns are driven by session.Turns, the same turn logic --what-if uses: one agent Run per recorded turn with that turn's first user
// message. Driving one Run per user message instead would desync the
// recorded replies at the first steering message, which is a second user
// message inside a turn rather than a turn of its own.
//
// The agent is read-only and rooted in an empty temporary directory — a
// replay must not touch the machine — so it is deliberately not the
// minimal agent evals and --what-if run.
func replaySession(ctx context.Context, path string, progress io.Writer) (recorded, now []session.Event, err error) {
	rp, err := replay.Load(path)
	if err != nil {
		return nil, nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	sess, _, err := session.Open(filepath.Dir(abs), strings.TrimSuffix(filepath.Base(abs), ".jsonl"))
	if err != nil {
		return nil, nil, err
	}
	events := sess.Events()
	turns := session.Turns(events)

	root, err := os.MkdirTemp("", "janetcode-replay-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(root)
	// The read tools run for real against the empty root. Every other tool
	// the recording called is a stub that runs nothing, so an edit or a
	// command replays as the same call without touching the machine.
	tools := []tool.Tool{
		tool.ReadFile{Root: root}, tool.ListDir{Root: root},
		tool.Glob{Root: root}, tool.Grep{Root: root},
	}
	have := map[string]bool{}
	for _, t := range tools {
		have[t.Name()] = true
	}
	for _, name := range modelCalls(events) {
		if !have[name] {
			have[name] = true
			tools = append(tools, replayStub{name: name})
		}
	}
	ag, err := agent.New(agent.Deps{
		Provider: rp,
		Log:      &session.Memory{},
		Tools:    tool.NewRegistry(tools...),
	}, agent.Settings{AutoApprove: true, MaxIterations: 200})
	if err != nil {
		return nil, nil, err
	}
	ag.System = prompt.System(root)
	fmt.Fprintf(progress, "replaying %s: %s, %s\n", filepath.Base(path), textfmt.Plural(len(turns), "turn"), textfmt.Plural(rp.Remaining(), "recorded model call"))
	for _, tp := range turns {
		if rp.Remaining() == 0 {
			break
		}
		if err := ag.Run(ctx, tp.Text, nil); err != nil && !errors.Is(err, agent.ErrMaxIterations) {
			fmt.Fprintf(progress, "  turn %d ended early: %v\n", tp.Turn, err)
			break
		}
	}
	return events, ag.Log.Events(), nil
}

// runReplay re-runs a recorded session with the model's part read from
// disk. Every bug fixed in this project's history that changed how a
// response is turned into tool calls would have shown up here as a
// different call sequence, for free and without a key.
func runReplay(path string) error {
	// A replay is only as good as the recording it replays: say first
	// whether the audit log still vouches for it.
	if rep, err := session.VerifyAudit(path); err == nil {
		fmt.Fprint(os.Stdout, auditSummary(rep))
	}
	recorded, now, err := replaySession(context.Background(), path, os.Stdout)
	if err != nil {
		return err
	}
	return compareCalls(modelCalls(recorded), modelCalls(now), os.Stdout)
}

// errReplayDiverged is what --replay exits with when the calls differ, so
// a script can gate on it without reading the report.
var errReplayDiverged = errors.New("replay: the call sequence changed; that is a regression unless it was intended")

// compareCalls prints the recorded and replayed call sequences and
// returns errReplayDiverged when they differ.
func compareCalls(want, got []string, w io.Writer) error {
	show := func(calls []string) string {
		if len(calls) == 0 {
			return "(no tool calls)"
		}
		return strings.Join(calls, " ")
	}
	fmt.Fprintf(w, "\nrecorded: %s\nnow:      %s\n", show(want), show(got))
	if strings.Join(want, " ") != strings.Join(got, " ") {
		return errReplayDiverged
	}
	fmt.Fprintln(w, "\nthe same calls in the same order: no behaviour change")
	return nil
}

// replayStub stands in for a tool that must not run during a replay.
type replayStub struct{ name string }

func (s replayStub) Name() string            { return s.name }
func (s replayStub) Description() string     { return "not run during a replay" }
func (s replayStub) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s replayStub) Mutating() bool          { return false }
func (s replayStub) Run(context.Context, json.RawMessage) (string, error) {
	return "(not run: replay)", nil
}
