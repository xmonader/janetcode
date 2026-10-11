package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
)

// TestDialogsDrawInATinyWindow: the connect dialog, the step dialog and
// the question box sized themselves as min(dialogWidth, width-4) and
// drew a rule of inner = that-4 cells, which is a negative count under
// eight columns: strings.Repeat panicked and took the program with it.
func TestDialogsDrawInATinyWindow(t *testing.T) {
	for w := 1; w <= 10; w++ {
		h := newHarness(t)
		cfg := &config.Config{Provider: "fake", Model: "m", Providers: map[string]config.Provider{
			"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
		}}
		h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { return &listing{}, nil })
		h.update(tea.WindowSizeMsg{Width: w, Height: 20})
		h.m.openConnect(stepPick)
		_ = h.m.View()
		h.m.closeConnect("")

		withMCP(t, h)
		h.m.openMCPAdd()
		_ = h.m.View()
		h.m.closeWizard()

		h.update(askMsg{ctx: context.Background(), questions: []agent.Question{{Prompt: "Which?", Options: []agent.AskOption{{Label: "a"}}}}, reply: make(chan []agent.Answer, 1)})
		_ = h.m.View()
	}
}
