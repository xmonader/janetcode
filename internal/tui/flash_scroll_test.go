package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/xmonader/janetcode/internal/agent"
)

func newScrolledFlash(t *testing.T) (*harness, string) {
	t.Helper()
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(saved) })
	t.Setenv("NO_COLOR", "")

	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 80, Height: 20})
	for i := 0; i < 60; i++ {
		h.m.transcript.info(fmt.Sprintf("filler %d", i))
	}
	h.m.refresh(false)
	h.update(tea.KeyMsg{Type: tea.KeyPgUp})
	if h.m.pinned {
		t.Fatal("pgup must unpin")
	}
	c := agent.ToolCall{ID: "1", Name: "bash"}
	h.m.transcript.apply(agent.Event{Kind: agent.EvToolRun, Call: &c})
	h.m.transcript.apply(agent.Event{Kind: agent.EvToolResult, Call: &c, Output: "ok"})
	h.m.refresh(false)
	if h.m.scheduleFlash() != nil {
		t.Fatal("no timer while scrolled up")
	}
	bright := h.m.styles.accent.Bold(true).Render("✓")
	if !strings.Contains(strings.Join(h.m.viewport.lines, "\n"), bright) {
		t.Fatal("test setup: the finished call is not drawn bright")
	}
	return h, bright
}

func pageToTail(h *harness) {
	h.t.Helper()
	for i := 0; i < 20 && !h.m.pinned; i++ {
		h.update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !h.m.pinned {
		h.t.Fatal("pgdn never reached the bottom")
	}
}

// TestFlashSettlesWhenTheReaderReturns: no flash timer runs while the
// reader is scrolled up, so a call that finished then kept its bright
// mark when they scrolled back down — until something else, perhaps
// nothing for the rest of an idle session, redrew the transcript.
func TestFlashSettlesWhenTheReaderReturns(t *testing.T) {
	h, bright := newScrolledFlash(t)
	h.m.transcript.findTool("1").ended = time.Now().Add(-time.Second)
	pageToTail(h)
	if strings.Contains(h.m.viewport.View(), bright) {
		t.Fatal("back at the tail, the faded call is still drawn bright")
	}
}

func TestFlashTimerRestartsWhenTheReaderReturns(t *testing.T) {
	h, bright := newScrolledFlash(t)
	pageToTail(h)
	if !h.m.flashPending {
		t.Fatal("returning to a live flash did not arm its fade timer")
	}
	h.m.transcript.findTool("1").ended = time.Now().Add(-time.Second)
	h.update(flashDoneMsg{})
	if strings.Contains(h.m.viewport.View(), bright) {
		t.Fatal("the restarted timer did not settle the flash")
	}
}
