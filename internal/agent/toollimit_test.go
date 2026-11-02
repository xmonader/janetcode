package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// The second call would pass the cap: it is answered unrun, the turn ends
// with ErrToolLimit, and the history stays sendable.
func TestToolLimitStopsBeforeTheCallThatPassesIt(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("r1", "read_file", `{"path":"a.txt"}`),
		toolCall("r2", "read_file", `{"path":"a.txt"}`),
		text("never reached"),
	}}
	a, _ := newAgent(t, f, nil)
	a.MaxToolCalls = 1
	err := a.Run(context.Background(), "read twice", nil)
	if !errors.Is(err, ErrToolLimit) {
		t.Fatalf("err = %v", err)
	}
	if calls, _ := a.Stats().ToolCalls(); calls != 1 || len(f.requests) != 3 { // the third is the closing reply
		t.Fatalf("tool calls = %d, model calls = %d", calls, len(f.requests))
	}
	last := beforeClosing(a.Messages)
	if last.Role != llm.RoleUser || len(last.Blocks) == 0 || last.Blocks[0].ToolUseID != "r2" {
		t.Fatalf("last message = %+v", last)
	}
}
