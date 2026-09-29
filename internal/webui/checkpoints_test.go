package webui

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// /checkpoints in the web is a card to pick from, not a text sheet: one
// row per restore point, newest first, each with the turn its preview
// button runs /undo turn for.
func TestWebCheckpointsIsAPicker(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{
		Provider: &scripted{responses: []*llm.Response{
			{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"one\n"}`)}}}, StopReason: llm.StopToolUse},
			{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
			{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t2", Name: "write_file", Input: json.RawMessage(`{"path":"b.txt","content":"two\n"}`)}}}, StopReason: llm.StopToolUse},
			{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
		}},
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Log: sess, Root: root, AutoApprove: true,
	}
	for _, p := range []string{"first", "second"} {
		if err := ag.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	s := newServer(context.Background(), Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root})
	if err := s.runCommand("/checkpoints"); err != nil {
		t.Fatal(err)
	}
	var rows []checkpointRow
	s.mu.Lock()
	for _, e := range s.entries {
		if e.Kind == "checkpoints" {
			rows, _ = e.Data.([]checkpointRow)
		}
	}
	s.mu.Unlock()
	if len(rows) != 2 || rows[0].Turn != 2 || rows[1].Turn != 1 || rows[0].ID == "" {
		t.Fatalf("checkpoints card rows = %+v, want turns 2 then 1", rows)
	}
}
