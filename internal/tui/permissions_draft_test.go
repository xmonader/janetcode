package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

// TestPromptKeepsTheDraftBeingTyped: a message typed into the composer
// while the turn ran stayed in the box when a permission prompt came up.
// The key line is drawn only in an empty box, so the prompt showed no
// keys at all; n then turned the box into the denial reason by wiping it,
// and the message was gone. It is put aside for the prompt now and comes
// back however the prompt closes.
func TestPromptKeepsTheDraftBeingTyped(t *testing.T) {
	for name, answer := range map[string]func(h *harness){
		"allow":         func(h *harness) { h.keys("y") },
		"deny":          func(h *harness) { h.keys("n"); h.typed("not that file"); h.enter() },
		"deny silently": func(h *harness) { h.keys("n"); h.update(tea.KeyMsg{Type: tea.KeyEsc}) },
		"cancel":        func(h *harness) { h.keys("n"); h.update(tea.KeyMsg{Type: tea.KeyCtrlC}) },
		"turn ends":     func(h *harness) { h.keys("n"); h.m.finishTurn(agentDoneMsg{}) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.update(tea.WindowSizeMsg{Width: 120, Height: 30})
			h.m.startTurn("thinking")
			h.typed("half a message")
			reply := make(chan verdict, 1)
			h.update(permissionMsg{call: agent.ToolCall{ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"a.go"}`)}, reply: reply})
			if v := plain(h.m.View()); !strings.Contains(v, "allow once") && !strings.Contains(v, "y once") {
				t.Fatalf("the prompt's keys are not on screen:\n%s", v)
			}
			answer(h)
			if h.m.pending != nil {
				t.Fatalf("the prompt is still up")
			}
			if got := h.m.input.Value(); got != "half a message" {
				t.Fatalf("composer = %q after the prompt closed, want the draft back", got)
			}
		})
	}
}

// TestCtrlCWhileGivingAReasonCancelsLikeC: ctrl+c while typing a denial
// reason cancels the turn, as c does at the prompt, but it answered with
// an ordinary denial: the agent took it for a refusal of this one call,
// and the next prompt of the same, now cancelled, turn was put up.
func TestCtrlCWhileGivingAReasonCancelsLikeC(t *testing.T) {
	h := newHarness(t)
	h.m.startTurn("thinking")
	ctx := context.Background()
	first := make(chan verdict, 1)
	h.update(permissionMsg{ctx: ctx, call: agent.ToolCall{ID: "c1", Name: "write_file"}, reply: first})
	h.update(permissionMsg{ctx: ctx, call: agent.ToolCall{ID: "c2", Name: "write_file"}, reply: make(chan verdict, 1)})
	h.keys("n")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if v := <-first; v.d != agent.Deny || v.reason != agent.CancelledReason {
		t.Fatalf("ctrl+c answered %+v, want a cancelling denial", v)
	}
	if !h.m.turn.cancelling {
		t.Fatalf("ctrl+c did not cancel the turn")
	}
}
