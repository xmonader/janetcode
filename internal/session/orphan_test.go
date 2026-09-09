package session

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// A session killed between a tool call and its result leaves an assistant
// message whose tool_use has no tool_result. Providers reject that history.
func TestFoldSurfaceOrphanToolUse(t *testing.T) {
	asst := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ID: "call_1", Name: "bash", Input: []byte(`{"command":"sleep 60"}`)},
	}}
	user := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "hi"}}}
	events := []Event{
		{Seq: 1, Type: TypeUserMessage, Surface: true, Message: &user},
		{Seq: 2, Type: TypeAssistant, Surface: true, Message: &asst},
		// process dies here: no tool/result event ever lands
	}
	msgs := FoldSurface(events)
	var pendingIDs []string
	answered := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				answered[b.ToolUseID] = true
			}
		}
	}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse && !answered[b.ID] {
				pendingIDs = append(pendingIDs, b.ID)
			}
		}
	}
	if len(pendingIDs) > 0 {
		t.Fatalf("resumed history has %d unanswered tool_use block(s) %v; providers reject this", len(pendingIDs), pendingIDs)
	}
	// The synthesised result must say the outcome is unknown rather than
	// implying the call did or did not happen.
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser || len(last.Blocks) != 1 || last.Blocks[0].ToolUseID != "call_1" {
		t.Fatalf("expected a synthesised result for call_1, got %+v", last)
	}
	if !last.Blocks[0].IsError || !strings.Contains(last.Blocks[0].Text, "unknown") {
		t.Fatalf("the synthesised result should be an error stating the outcome is unknown: %q", last.Blocks[0].Text)
	}
}

// A call that did get its result must not be answered twice.
func TestFoldSurfaceLeavesAnsweredCallsAlone(t *testing.T) {
	asst := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ID: "call_1", Name: "bash"},
		{Type: llm.BlockToolUse, ID: "call_2", Name: "bash"},
	}}
	res := llm.ToolResultMessage(llm.ToolResult("call_1", "ok", false))
	msgs := FoldSurface([]Event{
		{Seq: 1, Type: TypeAssistant, Surface: true, Message: &asst},
		{Seq: 2, Type: TypeToolResult, Surface: true, Message: &res},
	})
	var results int
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				results++
			}
		}
	}
	if results != 2 {
		t.Fatalf("want exactly one real and one synthesised result, got %d", results)
	}
}
