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
	// An unbreakable word under an indent is cut at the width and its
	// continuation keeps the indent: a word wrap put the indent on a
	// blank line of its own and the word at column 0 under it.
	url := fitWidth("  \x1b[34mhttps://example.com/"+strings.Repeat("a", 40)+"\x1b[0m", 20)
	for i, l := range strings.Split(url, "\n") {
		p := plain(l)
		if strings.TrimSpace(p) == "" || !strings.HasPrefix(p, "  ") || strings.HasPrefix(p, "   ") || lipgloss.Width(l) > 20 {
			t.Errorf("line %d of a broken URL is %q", i, p)
		}
		if !strings.Contains(l, "\x1b[34m") {
			t.Errorf("line %d of a broken URL lost its colour: %q", i, l)
		}
	}
	if s := "plain\tshort"; fitWidth(s, 40) != "plain"+tabStop+"short" {
		t.Fatalf("tab not expanded: %q", fitWidth(s, 40))
	}
}

// TestNoCursorControlReachesTheFrame: a progress bar's carriage return in
// a tool's output went to the terminal as it was, sending the cursor back
// to the left edge mid-frame so the rest of the line was drawn over the
// indent and the text before it. So did a BEL or a backspace, anywhere.
func TestNoCursorControlReachesTheFrame(t *testing.T) {
	h := newHarness(t)
	m := h.m
	call := &agent.ToolCall{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}
	m.transcript.apply(agent.Event{Kind: agent.EvToolStart, Call: call})
	m.transcript.apply(agent.Event{Kind: agent.EvToolResult, Call: call, Output: "progress 10%\rprogress 100%\r\nbell\x07 bad\b\b\bwow\n\x1b[31mred\x1b[0m 5%\r\x1b[31mred\x1b[0m 99%"})
	m.transcript.user("user\x07 text")
	m.transcript.apply(agent.Event{Kind: agent.EvText, Text: "reply\x07 text"})
	m.transcript.apply(agent.Event{Kind: agent.EvTurnDone})
	m.refresh(true)
	v := m.View()
	if strings.ContainsAny(v, "\r\x07\b") {
		t.Fatalf("a cursor control reached the frame: %q", v)
	}
	p := plain(v)
	for _, want := range []string{"progress 100%", "bell wow", "red 99%", "user text", "reply text"} {
		if !strings.Contains(p, want) {
			t.Errorf("frame lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "10%") || strings.Contains(p, "5%") {
		t.Errorf("an overwritten progress line survived:\n%s", p)
	}
}

func TestOverstrike(t *testing.T) {
	for in, want := range map[string]string{
		"AAAA 10%\rBB":    "BBAA 10%",
		"abc\b\bX":        "aXc",
		"x\x07y\x0bz":     "xyz",
		"plain":           "plain",
		"done\r":          "done",
		"\x1b[1mA\rB\x07": "B",
	} {
		if got := displayText(in); got != want {
			t.Errorf("displayText(%q) = %q, want %q", in, got, want)
		}
	}
}
