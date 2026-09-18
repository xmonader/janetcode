package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// A tier above the provider's default sits with the mode chip, and must
// not cost the status line its width discipline: the gauge still never
// yields, whatever the tier is called.
func TestStatusLineShowsEffort(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if v := plain(h.m.statusView()); strings.Contains(v, "effort") {
		t.Fatalf("the default tier should show nothing:\n%s", v)
	}
	h.m.opts.Agent.Effort = "max"
	if v := plain(h.m.statusView()); !strings.Contains(v, "effort:max") {
		t.Fatalf("the tier is not on the status line:\n%s", v)
	}
	for _, w := range []int{120, 80, 60, 40, 24} {
		h.update(tea.WindowSizeMsg{Width: w, Height: 24})
		line := h.m.statusView()
		if got := lipgloss.Width(line); got > w {
			t.Errorf("width %d: status line is %d columns:\n%s", w, got, plain(line))
		}
		if !gaugeWhole(h.m, line) {
			t.Errorf("width %d: no gauge wording survived whole:\n%s", w, plain(line))
		}
	}
}
