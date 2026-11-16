package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestCheckpointsDiffAndBadVerbInTheTerminal: "/checkpoints diff 1 now"
// shows the patch of what turn 1 left in the tree, and a bad verb is a
// usage error rather than silently opening the list.
func TestCheckpointsDiffAndBadVerbInTheTerminal(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: []byte(`{"path":"a.txt","content":"patched line\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.keys("write a.txt")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	h.keys("/checkpoints diff 1 now")
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "the work tree now") })
	if out := plain(h.m.View()); !strings.Contains(out, "before turn 1") {
		t.Fatalf("the diff does not say what was compared:\n%s", out)
	}
	h.keys("/checkpoints frobnicate")
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "unknown checkpoints action") })
}
