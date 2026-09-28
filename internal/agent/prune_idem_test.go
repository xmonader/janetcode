package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestPruneIsIdempotent holds that a second prune finds nothing left to
// do in results the first one already trimmed. Rewriting them again
// shaved a few dozen bytes off each, re-nested the omission markers,
// invalidated the cached prefix from the oldest trimmed result on, and
// reported the pass as having saved something.
func TestPruneIsIdempotent(t *testing.T) {
	for _, withArchive := range []bool{false, true} {
		long := strings.Repeat("line of output\n", 1000)
		a := &Agent{Provider: &fake{}, Tools: tool.NewRegistry(), Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "go"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "1", Name: "bash", Input: json.RawMessage(`{"command":"make"}`)}}},
			llm.ToolResultMessage(llm.ToolResult("1", long, false)),
			llm.TextMessage(llm.RoleAssistant, "a"), llm.TextMessage(llm.RoleUser, "b"),
			llm.TextMessage(llm.RoleAssistant, "c"), llm.TextMessage(llm.RoleUser, "d"),
			llm.TextMessage(llm.RoleAssistant, "e"), llm.TextMessage(llm.RoleUser, "f"),
		}}
		if withArchive {
			a.Archive = NewArchive(t.TempDir())
		}
		if a.Prune() == 0 {
			t.Fatalf("archive=%v: the first prune should trim", withArchive)
		}
		once := a.Messages[2].Blocks[0].Text
		if saved := a.Prune(); saved != 0 {
			t.Fatalf("archive=%v: a second prune saved %d bytes of an already trimmed result", withArchive, saved)
		}
		if got := a.Messages[2].Blocks[0].Text; got != once {
			t.Fatalf("archive=%v: a second prune rewrote the result:\n%q\nwas\n%q", withArchive, got, once)
		}
	}
}
