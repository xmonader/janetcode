package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)

// /effort changes the live session, so the tier the next request carries
// changes without rebuilding the provider.
func TestEffortCommandSetsTheLiveTier(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	if _, ok := lookup("/effort"); !ok {
		t.Fatal("/effort is not a command")
	}
	h.keys("/effort max")
	h.enter()
	if got := h.m.opts.Agent.Settings().Effort; got != "max" {
		t.Fatalf("effort = %q", got)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "effort max") {
		t.Fatalf("the change was not reported:\n%s", v)
	}

	h.keys("/effort down")
	h.enter()
	if got := h.m.opts.Agent.Settings().Effort; got != "xhigh" {
		t.Fatalf("after /effort down, effort = %q", got)
	}

	h.keys("/effort ultra")
	h.enter()
	if got := h.m.opts.Agent.Settings().Effort; got != "xhigh" {
		t.Fatalf("a bad tier was applied: %q", got)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "ultra") {
		t.Fatalf("a bad tier was not reported:\n%s", v)
	}

	// Alone it reports the tier and the ladder.
	h.keys("/effort")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "effort: xhigh") || !strings.Contains(v, "medium") {
		t.Fatalf("/effort alone said nothing useful:\n%s", v)
	}
}

// ctrl+r is to the reasoning tier what shift+tab is to the mode: the
// setting you change between one step of a task and the next.
func TestCtrlRCyclesEffort(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.m.opts.Agent.Effort = "xhigh"
	ctrlR := tea.KeyMsg{Type: tea.KeyCtrlR}
	h.update(ctrlR)
	if got := h.m.opts.Agent.Settings().Effort; got != "max" {
		t.Fatalf("effort = %q, want max", got)
	}
	h.update(ctrlR)
	if got := h.m.opts.Agent.Settings().Effort; got != "" {
		t.Fatalf("effort = %q, want the wrap to the provider default", got)
	}
	h.update(ctrlR)
	if got := h.m.opts.Agent.Settings().Effort; got != "low" {
		t.Fatalf("effort = %q, want low", got)
	}
	// The key must not have leaked into the prompt as text.
	if v := h.m.input.Value(); v != "" {
		t.Fatalf("ctrl+r typed %q into the input", v)
	}
}
