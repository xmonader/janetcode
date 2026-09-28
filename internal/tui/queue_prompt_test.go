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

// TestPasteWithCarriageReturnsIsLines: VTE and xterm send a pasted
// newline as a carriage return. A long paste was chipped with the CRs
// kept: "+1 line", one CR-joined line to the model, and CRs echoed to
// the terminal. Colour escapes copied from a terminal went the same way.
func TestPasteWithCarriageReturnsIsLines(t *testing.T) {
	h := newHarness(t)
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "\x1b[31merror\x1b[0m: something failed here")
	}
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Join(lines, "\r")), Paste: true})
	if got := h.m.input.Value(); !strings.Contains(got, "+60 lines]") {
		t.Fatalf("chip = %q, want 60 lines", got)
	}
	full := h.m.inflatePastes(h.m.input.Value())
	if strings.ContainsAny(full, "\r\x1b") {
		t.Fatalf("the chip keeps control characters: %q", full[:80])
	}
	if !strings.HasPrefix(full, "error: something failed here\nerror:") {
		t.Fatalf("inflated paste = %q", full[:80])
	}
}

// TestCommandArgumentsGetTheirPastes:a paste chip typed after a command
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
