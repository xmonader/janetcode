package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm/replay"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
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
	ag := &agent.Agent{
		Provider:    rp,
		Log:         &session.Memory{},
		AutoApprove: true,
		ReadOnly:    true,
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: root}, tool.ListDir{Root: root},
			tool.Glob{Root: root}, tool.Grep{Root: root},
		),
		System:        prompt.System(root),
		MaxIterations: 200,
	}
	fmt.Fprintf(progress, "replaying %s: %d turn(s), %d recorded model call(s)\n", filepath.Base(path), len(turns), rp.Remaining())
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
	recorded, now, err := replaySession(context.Background(), path, os.Stdout)
	if err != nil {
		return err
	}
	want, got := modelCalls(recorded), modelCalls(now)
	fmt.Fprintf(os.Stdout, "\nrecorded: %s\nnow:      %s\n", strings.Join(want, " "), strings.Join(got, " "))
	if strings.Join(want, " ") == strings.Join(got, " ") {
		fmt.Fprintln(os.Stdout, "\nthe same calls in the same order: no behaviour change")
		return nil
	}
	fmt.Fprintln(os.Stdout, "\nthe call sequence changed. That is a regression unless it was intended.")
	return nil
}
