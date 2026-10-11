package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/xmonader/janetcode/internal/llm"
)

// Tab completion offered "@my notes.go", but a mention ends at the first
// space, so what it inserted never attached. A path with a space is
// completed in the quoted form, which the mention parser reads back.
func TestMentionCompletionQuotesPathsWithSpaces(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	root := h.m.opts.Cwd
	if err := os.MkdirAll(filepath.Join(root, "my dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "my dir", "the notes.go"), []byte("package notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tab := tea.KeyMsg{Type: tea.KeyTab}
	h.keys("explain @my")
	h.update(tab)
	if got := h.m.input.Value(); got != `explain @"my dir/` {
		t.Fatalf("after tab: %q", got)
	}
	h.update(tab)
	if got := h.m.input.Value(); got != `explain @"my dir/the notes.go" ` {
		t.Fatalf("after second tab: %q", got)
	}
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	sent := h.m.opts.Agent.Messages[0].Blocks[0].Text
	if !strings.Contains(sent, "<attached path=\"my dir/the notes.go\">\npackage notes") {
		t.Fatalf("prompt sent = %q", sent)
	}
}
