package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/theme"
)

// TestRenderRowsShortWindowDrawsTheHighlight: the window search walked
// its start forward only from past the top, and without stopping at the
// highlighted row. A one-row window on the first row of a new section
// (the /connect list in a 13-row terminal, after one ↓) ran the start off
// the end of the list and panicked the program; a two-row window drew
// the section above and "2 of 22" with no highlighted row at all.
func TestRenderRowsShortWindowDrawsTheHighlight(t *testing.T) {
	st := newStyles(theme.Builtins()[0], false)
	rows := []row{
		{section: "configured", cells: []string{"openai", "OpenAI"}},
		{section: "add a provider", cells: []string{"anthropic", "Anthropic"}},
		{section: "add a provider", cells: []string{"openrouter", "OpenRouter"}},
		{section: "custom", cells: []string{"custom", "any server"}},
	}
	for budget := 1; budget <= 6; budget++ {
		for idx := range rows {
			out := renderRows(st, rows, idx, 60, budget)
			got := strings.Join(out, "\n")
			if !strings.Contains(got, "▸ ") || !strings.Contains(got, rows[idx].cells[0]) {
				t.Errorf("budget %d idx %d: highlighted row not drawn:\n%s", budget, idx, got)
			}
			if listed := len(out); listed > budget+1 {
				t.Errorf("budget %d idx %d: %d lines, more than the window and its counter", budget, idx, listed)
			}
		}
	}
	if out := renderRows(st, nil, 0, 60, 3); out != nil {
		t.Errorf("no rows drew %q", out)
	}
}
