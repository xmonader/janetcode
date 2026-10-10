package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// liveRecorder is the branch's live provider: it keeps every request.
type liveRecorder struct{ reqs []llm.Request }

func (l *liveRecorder) Name() string { return "live" }
func (l *liveRecorder) Stream(_ context.Context, req llm.Request, _ func(llm.Event)) (*llm.Response, error) {
	l.reqs = append(l.reqs, req)
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}, nil
}

// A branch replays the recorded prefix — the recorded call goes out under
// its recorded id, its tool runs again here — and its first live request
// carries that prefix unchanged plus the note, riding with the replayed
// tool results rather than as a message of its own.
func TestBranchFromReplaysThePrefixThenGoesLiveWithTheNote(t *testing.T) {
	sess, err := session.New(t.TempDir(), t.TempDir(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	recorded := []*llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "r1", Name: "probe", Input: json.RawMessage(`{}`)}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockText, Text: "the failed attempt's end"}}},
	}
	for _, m := range recorded {
		if _, err := sess.Append(session.Event{Type: session.TypeAssistant, Turn: 1, Surface: true, Message: m}); err != nil {
			t.Fatal(err)
		}
	}
	live := &liveRecorder{}
	ag := &agent.Agent{Provider: live, Tools: tool.NewRegistry(), Model: "m"}
	ag.CompactAt = 1000
	spec := &headless.AttemptSpec{Prompt: "fix it", Replay: sess.Path(), ReplayCalls: 1, Note: "[harness] An earlier attempt got stuck."}
	if err := branchFrom(ag, spec); err != nil {
		t.Fatal(err)
	}
	if ag.CompactAt != -1 {
		t.Fatalf("compaction is on while the recording plays: CompactAt = %d", ag.CompactAt)
	}
	if err := ag.Run(context.Background(), "fix it", nil); err != nil {
		t.Fatal(err)
	}
	if len(live.reqs) != 1 {
		t.Fatalf("live calls = %d, want 1: the recorded end must not be replayed", len(live.reqs))
	}
	if ag.CompactAt != 1000 {
		t.Fatalf("compaction was not restored at the switch: %d", ag.CompactAt)
	}
	msgs := live.reqs[0].Messages
	if len(msgs) != 3 {
		t.Fatalf("live request has %d messages, want prompt, recorded call, results+note", len(msgs))
	}
	if uses := msgs[1].ToolUses(); len(uses) != 1 || uses[0].ID != "r1" {
		t.Fatalf("the recorded call did not go out as recorded: %+v", msgs[1])
	}
	last := msgs[2]
	var result, note bool
	for _, b := range last.Blocks {
		switch {
		case b.Type == llm.BlockToolResult && b.ToolUseID == "r1":
			result = true
		case b.Type == llm.BlockText && strings.Contains(b.Text, "earlier attempt got stuck"):
			note = b.Synthetic
		}
	}
	if !result || !note {
		t.Fatalf("last message = %+v, want r1's result and the note marked synthetic", last)
	}
}

// No replay asked for: the agent is left alone (a second sample).
func TestBranchFromWithoutReplayLeavesTheProvider(t *testing.T) {
	live := &liveRecorder{}
	ag := &agent.Agent{Provider: live}
	if err := branchFrom(ag, &headless.AttemptSpec{Prompt: "x"}); err != nil || ag.Provider != llm.Provider(live) {
		t.Fatalf("err %v, provider %T", err, ag.Provider)
	}
}

func TestLastResult(t *testing.T) {
	out := []byte(`{"type":"result","result":"a","session_id":"s1"}` + "\n")
	r, err := lastResult(out)
	if err != nil || r.SessionID != "s1" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := lastResult([]byte("not json")); err == nil {
		t.Fatal("no result object should be an error")
	}
}
