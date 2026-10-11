package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

// TestDenyReasonPasteIsCleaned: a paste into the denial reason went to
// the textarea as the terminal sent it, without the cleaning the prompt
// gives a paste, so a CRLF paste arrived with every line break doubled
// and colour escapes as text.
func TestDenyReasonPasteIsCleaned(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.update(permissionMsg{call: agent.ToolCall{ID: "e1", Name: "write_file"}, reply: make(chan verdict, 1)})
	h.keys("n")
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("use\r\nthe \x1b[31mother\x1b[0m file"), Paste: true})
	if got, want := h.m.input.Value(), "use\nthe other file"; got != want {
		t.Fatalf("reason = %q, want %q", got, want)
	}
}
