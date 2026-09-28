package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

// TestAskDialogDropsControlCharacters is the same for the ask tool's
// questions and options, which are also the model's text.
func TestAskDialogDropsControlCharacters(t *testing.T) {
	h := newHarness(t)
	evil := "\x1b]52;c;ZWNobyBwd25lZA==\x07\x1b[2J"
	reply := make(chan []agent.Answer, 1)
	h.update(askMsg{questions: []agent.Question{
		{Prompt: "which?" + evil, Detail: "d" + evil, Options: []agent.AskOption{{Label: "a" + evil, Description: "x" + evil}}},
		{Prompt: "second" + evil, Options: []agent.AskOption{{Label: "b"}}},
	}, reply: reply})
	if out := h.m.View(); strings.Contains(out, "\x1b]52") || strings.Contains(out, "\x1b[2J") || !strings.Contains(out, "which?") {
		t.Fatalf("the ask dialog carries the model's escape sequences: %q", out)
	}
	// What is answered is still the option as the model wrote it.
	h.update(tea.KeyMsg{Type: tea.KeyEnter})
	h.update(tea.KeyMsg{Type: tea.KeyEnter})
	if a := <-reply; a[0].Label != "a"+evil {
		t.Fatalf("answer label = %q, want the option's own label", a[0].Label)
	}
}
