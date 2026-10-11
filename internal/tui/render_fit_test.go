package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
)

// TestNoTranscriptLineOverflowsTheViewport: a line wider than the window
// — a URL glamour cannot break, a tab-indented line of a /diff, tabs in
// a tool's output — was folded a second time by the viewport, after the
// transcript had counted its lines, so the bottom of a pinned transcript
// was pushed off the screen: the newest line was the one not shown.
func TestNoTranscriptLineOverflowsTheViewport(t *testing.T) {
	for _, w := range []int{40, 100} {
		h := newHarness(t)
		h.update(tea.WindowSizeMsg{Width: w, Height: 30})
		m := h.m
		m.transcript.code("# main.go  1 hunk\n \tif true {\n-\t\tfmt.Println(\"hello world this is a long line of text that goes on\")\n+\t\tfmt.Println(\"x\")")
		m.transcript.apply(agent.Event{Kind: agent.EvText, Text: "see https://example.com/" + strings.Repeat("a", 150) + "\n\n```go\nfunc main() {\n\tfmt.Println(\"" + strings.Repeat("q", 150) + "\")\n}\n```"})
		m.transcript.apply(agent.Event{Kind: agent.EvTurnDone})
		call := &agent.ToolCall{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}
		m.transcript.apply(agent.Event{Kind: agent.EvToolStart, Call: call})
		m.transcript.apply(agent.Event{Kind: agent.EvToolResult, Call: call, Output: "a\t\t\t\t\t\t\t\t\t\t\t\tb\n" + strings.Repeat("n", 300)})
		m.transcript.info("THE LAST LINE")
		m.refresh(true)
		for i, l := range m.viewport.lines {
			if strings.Contains(l, "\t") || lipgloss.Width(l) > w {
				t.Errorf("width %d: line %d is %d columns (tab: %v): %q", w, i, lipgloss.Width(l), strings.Contains(l, "\t"), plain(l))
			}
		}
		if v := plain(m.viewport.View()); !strings.Contains(v, "THE LAST LINE") {
			t.Errorf("width %d: the pinned view does not show the last line:\n%s", w, v)
		}
	}
}

// TestTightWindowStartsOnContent: the banner dropped in a tight window
// left its separator behind as a blank first row.
func TestTightWindowStartsOnContent(t *testing.T) {
	h := newHarness(t)
	h.m.transcript.info("first words")
	h.update(tea.WindowSizeMsg{Width: 90, Height: 14})
	if !h.m.tight() {
		t.Fatal("a 14-row window is tight")
	}
	if l := plain(h.m.viewport.lines[0]); !strings.Contains(l, "first words") {
		t.Fatalf("first transcript row is %q, want the first entry", l)
	}
}

// TestFitWidthReopensTheStyleAfterABreak:a styled run broken across two
// lines is closed at the end of the first and reopened on the second, so
// a line scrolled to the top of the view keeps its colour.
func TestFitWidthReopensTheStyleAfterABreak(t *testing.T) {
	in := "\x1b[31m" + strings.Repeat("r", 15) + "\x1b[0m tail"
	got := strings.Split(fitWidth(in, 10), "\n")
	if len(got) < 2 {
		t.Fatalf("not broken: %q", got)
	}
	if !strings.HasSuffix(got[0], "\x1b[0m") || !strings.HasPrefix(got[1], "\x1b[31m") {
		t.Fatalf("style not carried over the break: %q", got)
	}
	for _, l := range got {
		if lipgloss.Width(l) > 10 {
			t.Fatalf("line wider than 10: %q", l)
		}
	}
	if s := "plain\tshort"; fitWidth(s, 40) != "plain"+tabStop+"short" {
		t.Fatalf("tab not expanded: %q", fitWidth(s, 40))
	}
}
