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

// TestPruneKnowsWhichCallAResultAnswers covers a server that numbers its
// calls per response: every call arrives as call_0, the harness renames
// the later ones (call_0#2, …) and their results go back under call_0.
// Pruning looked results up by that shared id, so every result was taken
// for the first call's: a read of b.go was stubbed out as superseded
// because a.go had been read again, and a placeholder named the wrong
// call.
func TestPruneKnowsWhichCallAResultAnswers(t *testing.T) {
	root := t.TempDir()
	reg := tool.NewRegistry(tool.ReadFile{Root: root})
	long := strings.Repeat("content line\n", 400)
	use := func(id, reply, path string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, ReplyID: reply, Name: "read_file", Input: json.RawMessage(`{"path":"` + path + `"}`)}}}
	}
	res := func(text string) llm.Message { return llm.ToolResultMessage(llm.ToolResult("call_0", text, false)) }
	msgs := func() []llm.Message {
		return []llm.Message{
			llm.TextMessage(llm.RoleUser, "go"),
			use("call_0", "", "a.go"), res("A " + long),
			use("call_0#2", "call_0", "b.go"), res("B " + long),
			use("call_0#3", "call_0", "a.go"), res("A " + long),
			llm.TextMessage(llm.RoleAssistant, "a"), llm.TextMessage(llm.RoleUser, "b"),
			llm.TextMessage(llm.RoleAssistant, "c"), llm.TextMessage(llm.RoleUser, "d"),
			llm.TextMessage(llm.RoleAssistant, "e"), llm.TextMessage(llm.RoleUser, "f"),
		}
	}
	a := &Agent{Provider: &fake{}, Tools: reg, Messages: msgs()}
	a.Prune()
	if got := a.Messages[2].Blocks[0].Text; got != superseded {
		t.Fatalf("the first read of a.go was read again later and should be superseded: %.60q", got)
	}
	if got := a.Messages[4].Blocks[0].Text; !strings.HasPrefix(got, "B ") {
		t.Fatalf("the only read of b.go was taken for a superseded read of a.go: %.60q", got)
	}

	b := &Agent{Provider: &fake{}, Tools: reg, Messages: msgs()}
	b.pruneTo(1)
	if got := b.Messages[4].Blocks[0].Text; !strings.Contains(got, "read_file b.go") {
		t.Fatalf("the placeholder names another call: %q", got)
	}
}
