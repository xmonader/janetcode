package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
)
// TestPastedPromptHistoryKeepsThePaste: a prompt sent with a paste chip
// was written to the history file as the chip. The chip numbering starts
// again in the next process, so recalling it there sent whatever had been
// pasted first in that process instead, or the literal chip text.
func TestPastedPromptHistoryKeepsThePaste(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	paste := strings.Repeat("line\n", 20) + "end"
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	got := loadPromptHistory(h.m.hist.file)
	if len(got) != 1 || got[0].Text != paste {
		t.Fatalf("history file = %+v, want the pasted text", got)
	}
	// This process still recalls the chip, which it can still inflate.
	if s, _ := h.m.hist.back(""); !strings.HasPrefix(s, "[paste #1 ") {
		t.Fatalf("in-process recall = %q, want the chip", s)
	}
}
