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

// TestCommandArgumentsGetTheirPastes: a paste chip typed after a command
// ("/btw " then a pasted stack trace) reached the command as the chip.
// /btw asked the model about "[paste #1 +30 lines]", and /goal and /loop
// stored the chip as their objective, to be re-sent long after the
// process that knew what it stood for was gone.
func TestCommandArgumentsGetTheirPastes(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	paste := strings.Repeat("at frame\n", 30) + "panic: boom"
	h.keys("/btw ")
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(paste), Paste: true})
	h.enter()
	prov := h.m.opts.Agent.Provider.(*scripted)
	h.pump(func() bool { return len(prov.reqs()) > 0 })
	req := prov.reqs()[0]
	last := req.Messages[len(req.Messages)-1]
	var text string
	for _, b := range last.Blocks {
		text += b.Text
	}
	if !strings.Contains(text, "panic: boom") || strings.Contains(text, "[paste #") {
		t.Fatalf("/btw sent %q, want the pasted text", text)
	}
}
