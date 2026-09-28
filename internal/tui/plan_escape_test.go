package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

// TestPlanStripDropsControlCharacters: the plan's steps are the model's
// text, which can carry what a file it read told it to write. The strip
// above the input and the /todos panel wrote them to the terminal raw, so
// a step holding an escape sequence (clear the screen, set the clipboard
// with OSC 52) was obeyed by the terminal instead of shown.
func TestPlanStripDropsControlCharacters(t *testing.T) {
	h := newHarness(t)
	evil := "run tests\x1b]52;c;ZWNobyBwd25lZA==\x07\x1b[2J"
	list, _ := json.Marshal([]agent.Todo{
		{Text: evil, Status: agent.TodoDoing},
		{Text: "ship", Status: "pending", BlockedBy: []string{evil}},
	})
	h.m.opts.Agent.LogEvent(session.Event{Type: session.TypeTodoWrite, Extra: list})
	h.m.layout()
	for name, out := range map[string]string{"plan strip": h.m.planView(), "view": h.m.View()} {
		if strings.Contains(out, "\x1b]52") || strings.Contains(out, "\x1b[2J") {
			t.Fatalf("the %s carries the model's escape sequences: %q", name, out)
		}
		if !strings.Contains(out, "run tests") {
			t.Fatalf("the %s lost the step's text: %q", name, out)
		}
	}
	h.m.openOverlay(overlayTodos)
	if out := h.m.View(); strings.Contains(out, "\x1b]52") || strings.Contains(out, "\x1b[2J") {
		t.Fatalf("/todos carries the model's escape sequences: %q", out)
	}
}
