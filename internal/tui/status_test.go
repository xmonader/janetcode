package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)

// The status line carries the session's token counts and the figures the
// context percentage is of, and says when a turn has gone quiet — a slow
// endpoint spent two and a half minutes on one call, and a spinner alone
// says the same thing there as on a fast one.
func TestStatusLineShowsTokensContextAndQuietTime(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hi"), StopReason: llm.StopEndTurn,
		Usage: llm.Usage{InputTokens: 12000, OutputTokens: 3400, CacheReadTokens: 9000}})
	h.update(tea.WindowSizeMsg{Width: 160, Height: 40})
	h.keys("hello")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	line := plain(h.m.statusView())
	for _, want := range []string{"↑12k", "↓3.4k", "⧉9.0k", "/"} {
		if !strings.Contains(line, want) {
			t.Fatalf("status line lacks %q:\n%s", want, line)
		}
	}
	if !strings.Contains(line, "ctx ") || !strings.Contains(line, "%") {
		t.Fatalf("no context gauge:\n%s", line)
	}

	// A running turn that has produced nothing for a while says so; one
	// that is still streaming does not.
	h.m.turn.running = true
	h.m.turn.startedAt = time.Now().Add(-2 * time.Minute)
	h.m.turn.lastEventAt = time.Now().Add(-90 * time.Second)
	if got := plain(h.m.statusView()); !strings.Contains(got, "quiet 1m30s") {
		t.Fatalf("a turn silent for 90s:\n%s", got)
	}
	h.m.turn.lastEventAt = time.Now()
	if got := plain(h.m.statusView()); strings.Contains(got, "quiet") {
		t.Fatalf("a turn still streaming:\n%s", got)
	}
	h.m.turn.running = false
}
