package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm/replay"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// diagnoseNote turns language-server diagnostics for a just-edited file
// into the note appended to the tool result.
// recordedCalls is the tool calls a session made, in order, read straight
// from its log.
func recordedCalls(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var e session.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Type == session.TypeToolCall && !strings.HasPrefix(e.CallID, "script-") {
			out = append(out, e.Name)
		}
	}
	return out, sc.Err()
}

// runReplay re-runs a recorded session with the model's part read from
// disk. Every bug fixed in this project's history that changed how a
// response is turned into tool calls would have shown up here as a
// different call sequence, for free and without a key.
func runReplay(path string) error {
	rp, err := replay.Load(path)
	if err != nil {
		return err
	}
	want, err := recordedCalls(path)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "janetcode-replay-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)

	var got []string
	ag := &agent.Agent{
		Provider:    rp,
		Log:         &session.Memory{},
		AutoApprove: true,
		ReadOnly:    true, // a replay must not touch the machine
		Tools: tool.NewRegistry(
			tool.ReadFile{Root: root}, tool.ListDir{Root: root},
			tool.Glob{Root: root}, tool.Grep{Root: root},
		),
		System:        prompt.System(root),
		MaxIterations: 200,
	}
	ctx := context.Background()
	prompts := rp.Prompts()
	fmt.Fprintf(os.Stdout, "replaying %s: %d prompt(s), %d recorded model call(s)\n", filepath.Base(path), len(prompts), rp.Remaining())
	for _, p := range prompts {
		if rp.Remaining() == 0 {
			break
		}
		err := ag.Run(ctx, p, func(e agent.Event) {
			if e.Kind == agent.EvToolRun && e.Call != nil {
				got = append(got, e.Call.Name)
			}
		})
		if err != nil && !errors.Is(err, agent.ErrMaxIterations) {
			fmt.Fprintf(os.Stdout, "  turn ended early: %v\n", err)
			break
		}
	}
	fmt.Fprintf(os.Stdout, "\nrecorded: %s\nnow:      %s\n", strings.Join(want, " "), strings.Join(got, " "))
	if strings.Join(want, " ") == strings.Join(got, " ") {
		fmt.Fprintln(os.Stdout, "\nthe same calls in the same order: no behaviour change")
		return nil
	}
	fmt.Fprintln(os.Stdout, "\nthe call sequence changed. That is a regression unless it was intended.")
	return nil
}
