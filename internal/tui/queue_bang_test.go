package tui

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestQueuedBangCommandRunsAsACommand: "!go test ./..." typed while a
// turn runs is queued like a prompt, but when the turn ended the queue
// sent it to the model as the text "!go test ./...", which runs nothing
// and costs a turn of the model guessing what the bang means.
func TestQueuedBangCommandRunsAsACommand(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.m.startTurn("thinking")
	h.typed("!echo queued-bang")
	h.enter()
	if len(h.m.turn.queued) != 1 {
		t.Fatalf("queued = %q", h.m.turn.queued)
	}
	h.m.finishTurn(agentDoneMsg{})
	if h.m.hist.lastUser == "!echo queued-bang" {
		t.Fatal("the queued command was sent to the model as a prompt")
	}
	if !h.m.turn.running || h.m.turn.phase != "running your command" {
		t.Fatalf("the queued command did not run: running=%v phase=%q", h.m.turn.running, h.m.turn.phase)
	}
	h.pump(func() bool { return !h.m.turn.running })
}
