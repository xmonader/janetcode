package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestCancelledMCPPromptIsNotSent: esc while an MCP prompt was being
// rendered said "cancelling…" but stopped nothing — the request had no
// cancel the turn could reach — and when the rendering came back it was
// submitted as a turn all the same. The rendering is cancelled now, and
// what comes back after esc is dropped.
func TestCancelledMCPPromptIsNotSent(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.m.startTurn("rendering prompt from docs")
	ctx, cancel := context.WithCancel(context.Background())
	h.m.turn.cancel = cancel
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if ctx.Err() == nil {
		t.Fatal("esc did not cancel the rendering")
	}
	h.update(mcpPromptMsg{command: "/mcp__docs__review", text: "review the diff"})
	if h.m.turn.running || h.m.turn.cancelling {
		t.Fatalf("after a cancelled rendering running=%v cancelling=%v; want the turn over and nothing sent", h.m.turn.running, h.m.turn.cancelling)
	}
	if h.m.hist.lastUser == "review the diff" {
		t.Fatal("the cancelled prompt was submitted")
	}

	// Not cancelled, the rendered text is the user's turn.
	h.m.startTurn("rendering prompt from docs")
	h.update(mcpPromptMsg{command: "/mcp__docs__review", text: "review the diff"})
	if !h.m.turn.running || h.m.hist.lastUser != "review the diff" {
		t.Fatalf("a finished rendering was not sent: running=%v last=%q", h.m.turn.running, h.m.hist.lastUser)
	}
}
