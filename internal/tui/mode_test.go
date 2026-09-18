package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

// allModes is every mode, so a new one added to the enum without a test
// entry here shows up as a failure rather than as silence.
var allModes = []uiMode{modePrompt, modeAsk, modeOverlay, modePicker, modeConnect, modeWizard, modeApprove, modeDeny}

// TestEveryModeHasAKeyHandler: handleKey indexes modeKeys with mode(),
// so a mode with no entry is a nil call on the next key press.
func TestEveryModeHasAKeyHandler(t *testing.T) {
	for _, k := range allModes {
		if k == modePrompt {
			if _, ok := modeKeys[k]; ok {
				t.Errorf("modePrompt must fall through to the prompt key table, not modeKeys")
			}
			continue
		}
		if modeKeys[k] == nil {
			t.Errorf("mode %s has no key handler", k)
		}
	}
	if len(modeKeys) != len(allModes)-1 {
		t.Errorf("modeKeys has %d entries for %d non-prompt modes", len(modeKeys), len(allModes)-1)
	}
}

// TestModeStringIsNamed keeps the diagnostic honest.
func TestModeStringIsNamed(t *testing.T) {
	for _, k := range allModes {
		if k.String() == "unknown" {
			t.Errorf("mode %d has no name", k)
		}
	}
}

// TestModeTransitions walks the state field by field: what the window is
// in, what it goes to, and that it comes back to the prompt.
func TestModeTransitions(t *testing.T) {
	h := newHarness(t)
	m := h.m
	if got := m.mode(); got != modePrompt {
		t.Fatalf("a fresh window is in %s, want prompt", got)
	}

	call := agent.ToolCall{ID: "c1", Name: "bash"}
	reply := make(chan verdict, 1)
	m.pending = &permissionMsg{call: call, reply: reply}
	if got := m.mode(); got != modeApprove {
		t.Fatalf("with a prompt up the mode is %s, want approve", got)
	}
	m.denying = true
	if got := m.mode(); got != modeDeny {
		t.Fatalf("collecting a reason the mode is %s, want deny", got)
	}
	m.answer(agent.Deny, "because")
	if got := m.mode(); got != modePrompt {
		t.Fatalf("after answering the mode is %s, want prompt", got)
	}
	if m.denying {
		t.Error("answering left the window collecting a reason")
	}

	// The dialogs, each in turn, and each back to the prompt.
	for _, tc := range []struct {
		want  uiMode
		open  func()
		close func()
	}{
		{modeWizard, func() { m.wiz = &wizard{} }, func() { m.wiz = nil }},
		{modeConnect, func() { m.connect = &connect{} }, func() { m.connect = nil }},
		{modePicker, func() { m.picker = &picker{} }, func() { m.picker = nil }},
		{modeOverlay, func() { m.ov.kind = overlayJobs }, func() { m.ov.kind = "" }},
		{modeAsk, func() { m.asking = &askState{} }, func() { m.asking = nil }},
	} {
		tc.open()
		if got := m.mode(); got != tc.want {
			t.Fatalf("mode = %s, want %s", got, tc.want)
		}
		if !tc.want.modal() {
			t.Errorf("%s should be a modal", tc.want)
		}
		if !tc.want.blocked() {
			t.Errorf("%s should block the prompt", tc.want)
		}
		tc.close()
		if got := m.mode(); got != modePrompt {
			t.Fatalf("closing the %s left the window in %s", tc.want, got)
		}
	}
}

// TestModePriorityIsOneOrder: two modals up at once must be drawn by the
// same one that is reading the keyboard. The picker is always the
// innermost modal — /trajectory's search opens one over the ledger — so
// it outranks the overlay in mode(), and View reads the same mode().
func TestModePriorityIsOneOrder(t *testing.T) {
	h := newHarness(t)
	m := h.m
	m.ov.kind = overlayJobs
	m.picker = &picker{title: "a picker over the overlay"}
	if got := m.mode(); got != modePicker {
		t.Fatalf("mode = %s, want picker", got)
	}
	if got := plain(m.View()); !strings.Contains(got, "a picker over the overlay") {
		t.Error("the overlay was drawn while the picker was taking the keys")
	}
	m.picker = nil
	if got := m.mode(); got != modeOverlay {
		t.Fatalf("closing the picker should hand the keys back to the overlay, got %s", got)
	}
}

// TestAnOpenWizardBlocksTheRestOfTheWindow covers the parts that used to
// ignore it: the completion menu, the plan strip and the hint engine each
// re-derived "is a modal up" and all three left the wizard out.
func TestAnOpenWizardBlocksTheRestOfTheWindow(t *testing.T) {
	h := newHarness(t)
	m := h.m
	m.input.SetValue("/he")
	if len(m.completions()) == 0 {
		t.Fatal("precondition: /he should offer completions at the prompt")
	}
	m.wiz = &wizard{title: "Add an MCP server"}
	if got := m.completions(); got != nil {
		t.Errorf("the completion menu is still open behind the wizard: %d rows", len(got))
	}
	if got := m.planHeight(); got != 0 {
		t.Errorf("the plan strip claims %d rows behind the wizard", got)
	}
	m.refreshHintState()
	if !m.hint.state.Blocked {
		t.Error("the hint engine thinks the user is idle while the wizard is up")
	}
}

// TestDenyingIsClearedWhenTheTurnEnds: a turn cancelled while the user
// was typing a denial reason used to leave the window collecting one
// with no prompt behind it, so the next call's approval prompt came up
// already in deny mode and y/n went into the textarea.
func TestDenyingIsClearedWhenTheTurnEnds(t *testing.T) {
	h := newHarness(t)
	m := h.m
	m.pending = &permissionMsg{call: agent.ToolCall{ID: "c1", Name: "bash"}, reply: make(chan verdict, 1)}
	m.denying = true
	m.finishTurn(agentDoneMsg{err: context.Canceled})
	if m.denying {
		t.Fatal("the turn ended but the window is still collecting a denial reason")
	}
	if got := m.mode(); got != modePrompt {
		t.Fatalf("after the turn the mode is %s, want prompt", got)
	}

	// And the next prompt comes up asking, not denying.
	m.pending = &permissionMsg{call: agent.ToolCall{ID: "c2", Name: "bash"}, reply: make(chan verdict, 1)}
	if got := m.mode(); got != modeApprove {
		t.Fatalf("the next prompt is in %s, want approve", got)
	}
	if _, handled := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}); !handled {
		t.Error("y at an approval prompt was not taken by the prompt")
	}
}
