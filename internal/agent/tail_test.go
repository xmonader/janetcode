package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// assistantCall is an assistant turn that calls one tool.
func assistantCall(id, text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockText, Text: text},
		{Type: llm.BlockToolUse, ID: id, Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)},
	}}
}

// The whole point of tailStart is that the kept tail is a valid
// conversation on its own. A tail that begins with a tool result whose
// tool_use was summarised away is rejected by every provider, so the cut
// is only ever made at an assistant message.
func TestTailStartNeverCutsACallFromItsResult(t *testing.T) {
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "start"),
		assistantCall("c1", "looking"),
		llm.ToolResultMessage(llm.ToolResult("c1", "contents", false)),
		assistantCall("c2", "and again"),
		llm.ToolResultMessage(llm.ToolResult("c2", "more contents", false)),
		llm.TextMessage(llm.RoleAssistant, "done"),
	}
	ts := tailStart(msgs)
	if ts >= len(msgs) {
		t.Fatalf("tailStart kept nothing (%d of %d)", ts, len(msgs))
	}
	if msgs[ts].Role != llm.RoleAssistant {
		t.Fatalf("the tail starts at a %s message, so its tool results have no call", msgs[ts].Role)
	}
	// Every tool result in the tail has its call in the tail too.
	calls := map[string]bool{}
	for _, m := range msgs[ts:] {
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockToolUse:
				calls[b.ID] = true
			case llm.BlockToolResult:
				if !calls[b.ToolUseID] {
					t.Fatalf("tool result %q is in the tail but its call is not", b.ToolUseID)
				}
			}
		}
	}
}

// tailMessages caps how far back the tail reaches, whatever the sizes.
func TestTailStartStopsAtTheMessageCap(t *testing.T) {
	var msgs []llm.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs, llm.TextMessage(llm.RoleAssistant, "a"))
	}
	if ts := tailStart(msgs); len(msgs)-ts > tailMessages {
		t.Fatalf("the tail reaches back %d messages, the cap is %d", len(msgs)-ts, tailMessages)
	}
}

// tailTokens caps how much the tail may cost. A huge recent tool result
// must not drag the whole window into the kept tail — compaction would
// then free nothing and the next turn would compact again.
func TestTailStartStopsAtTheTokenBudget(t *testing.T) {
	big := strings.Repeat("x ", tailTokens*4) // far more than the budget
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleAssistant, "oldest candidate"),
		llm.TextMessage(llm.RoleUser, "u"),
		llm.TextMessage(llm.RoleAssistant, big),
		llm.TextMessage(llm.RoleUser, "u"),
		llm.TextMessage(llm.RoleAssistant, "newest"),
	}
	ts := tailStart(msgs)
	kept := 0
	for _, m := range msgs[ts:] {
		kept += messageTokens(m)
	}
	if kept > tailTokens {
		t.Fatalf("the tail costs %d tokens, the budget is %d", kept, tailTokens)
	}
	// The walk must have stopped at the oversized message rather than
	// stepping over it: everything at or before it stays in the head, or
	// compaction frees nothing and the next turn compacts again.
	if ts <= 2 {
		t.Fatalf("tailStart = %d: the oversized message was kept in the tail", ts)
	}
}

// A tail of one enormous message costs more than the budget on its own.
// The walk breaks before recording anything, so nothing is kept verbatim
// and the caller summarises the lot — which is the intended outcome, but
// only because the tail is empty rather than silently oversized.
func TestTailStartKeepsNothingWhenTheNewestMessageBlowsTheBudget(t *testing.T) {
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "start"),
		llm.TextMessage(llm.RoleAssistant, strings.Repeat("x ", tailTokens*2)),
	}
	if ts := tailStart(msgs); ts != len(msgs) {
		t.Fatalf("tailStart = %d, want %d: a tail over budget must be kept empty", ts, len(msgs))
	}
}
