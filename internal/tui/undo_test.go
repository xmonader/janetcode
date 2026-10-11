package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// /undo that also takes the turn out of the conversation said so on the
// status line, and the very next line overwrote it: the user never learned
// the model had forgotten the turn.
func TestUndoSaysTheTurnLeftTheContext(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "written"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.m.command("/undo")
	if h.m.opts.Agent.LastUndoRemoved() == 0 {
		t.Skip("this undo kept the conversation")
	}
	if !strings.Contains(h.m.status, "the turn's") {
		t.Fatalf("status does not say the turn left the context: %q", h.m.status)
	}
}
