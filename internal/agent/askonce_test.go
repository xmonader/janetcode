package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// A /btw asked while a tool is running reads a history that ends in the
// assistant's tool call with no result yet. Sent as it stood, with the
// question after it, the request has a tool_use no tool_result answers,
// which every provider refuses.
func TestAskOnceMidToolCallPairsTheCall(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("answer")}}
	a, _ := newAgent(t, f, nil)
	a.Messages = []llm.Message{
		llm.TextMessage(llm.RoleUser, "run the tests"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"go test"}`)}}},
	}
	if _, err := a.AskOnce(context.Background(), "what are you doing?"); err != nil {
		t.Fatal(err)
	}
	msgs := f.requests[0].Messages
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser {
		t.Fatalf("last message role %q, want user", last.Role)
	}
	var paired, asked bool
	for _, b := range last.Blocks {
		if b.Type == llm.BlockToolResult && b.ToolUseID == "c1" {
			paired = true
		}
		if b.Type == llm.BlockText && b.Text == "what are you doing?" {
			asked = true
		}
	}
	if !paired || !asked {
		t.Fatalf("the call is answered: %v, the question is asked: %v; last message %+v", paired, asked, last)
	}
	if len(a.Messages) != 2 {
		t.Fatalf("AskOnce changed the history: %d messages", len(a.Messages))
	}
}

// A history that already ends in a user message (a turn that errored, or
// tool results whose model call is in flight) takes the question into
// that message rather than following it with a second user message.
func TestAskOnceAfterUserMessageMerges(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("answer")}}
	a, _ := newAgent(t, f, nil)
	a.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}
	if _, err := a.AskOnce(context.Background(), "btw?"); err != nil {
		t.Fatal(err)
	}
	msgs := f.requests[0].Messages
	if len(msgs) != 1 || len(msgs[0].Blocks) != 2 {
		t.Fatalf("want one user message holding both texts, got %+v", msgs)
	}
	if len(a.Messages[0].Blocks) != 1 {
		t.Fatalf("AskOnce changed the history: %+v", a.Messages[0])
	}
}
