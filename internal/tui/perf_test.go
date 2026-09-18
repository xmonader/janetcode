package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)

// "It felt slow" is a real report, and a transcript cannot answer it: a
// provider taking 40 seconds a call and a harness taking 40 milliseconds
// look the same on screen. /perf divides the wall clock.
func TestPerfCommandReportsWhereTheTimeWent(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn})
	h.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.keys("do the thing")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	h.keys("/perf")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"where the time went", "model", "tools", "you", "harness"} {
		if !strings.Contains(v, want) {
			t.Fatalf("/perf left out %q:\n%s", want, v)
		}
	}
	if st := h.m.opts.Agent.Stats(); st.TurnTime <= 0 {
		t.Fatalf("the turn was never timed: %+v", st)
	}
}

// Before anything has run there is no time to divide, and the report
// says so rather than printing a page of zeroes.
func TestPerfCommandOnAFreshSession(t *testing.T) {
	h := newHarness(t)
	h.keys("/perf")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no turn") {
		t.Fatalf("a fresh /perf:\n%s", v)
	}
}
