package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestMCPAddChangingTransportDropsTheOldPairs: the settings typed for a
// stdio command are environment variables. Going back and choosing http
// kept them, and the empty enter that ends the settings step folded them
// into the remote server's request headers — an API token meant for a
// local process sent to a host somewhere else.
func TestMCPAddChangingTransportDropsTheOldPairs(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	withMCP(t, h)
	h.keys("/mcp add")
	h.enter()
	for _, typed := range []string{"docs", "", "never-run --serve", "API_TOKEN=secret"} {
		h.typed(typed)
		h.enter()
	}
	esc := func() { h.update(tea.KeyMsg{Type: tea.KeyEsc}) }
	esc() // settings → command
	esc() // command → transport
	if got := h.m.wiz.step().name; got != "transport" {
		t.Fatalf("backed out to %q, want the transport step", got)
	}
	for _, typed := range []string{"http", "https://example.invalid/mcp", ""} {
		h.typed(typed)
		h.enter()
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "https://example.invalid/mcp") {
		t.Fatalf("not at the confirmation:\n%s", v)
	}
	if strings.Contains(v, "header") {
		t.Fatalf("the stdio env var became a header of the remote server:\n%s", v)
	}
}
