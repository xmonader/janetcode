package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEditResultDropsTheWindowKeepsTheNote: the transcript has already
// shown the change as a diff, so the numbered window an edit appends says
// it twice — but a note the harness added after the window (what the
// language server made of the change, or why there was none) is the one
// thing to read next and must survive.
func TestEditResultDropsTheWindowKeepsTheNote(t *testing.T) {
	out := "Edited main.go (1 replacement, +1 -1 lines)\n" +
		"replaced line 5: func Add(a, b int) int { return a + b }\n\n" +
		"     1\tpackage main\n     2\t\n     3\tfunc Add(a, b int) int { return a + b + 1 }\n" +
		"[diagnostics unavailable: lsp: gopls is not installed]"
	got := strings.Join(EditFile{}.RenderResult(json.RawMessage(`{}`), out, RenderContext{}), "\n")
	if strings.Contains(got, "package main") {
		t.Fatalf("the numbered window survived:\n%s", got)
	}
	if !strings.Contains(got, "Edited main.go") || !strings.Contains(got, "diagnostics unavailable") {
		t.Fatalf("the summary or the note was lost:\n%s", got)
	}
	// A result that is not a window is left exactly as it is.
	plain := "Edited a.go\n\nsomething else entirely"
	if lines := (EditFile{}).RenderResult(json.RawMessage(`{}`), plain, RenderContext{}); lines != nil {
		t.Fatalf("a plain result was rewritten: %q", lines)
	}
}

// TestBashKeepsItsTail and the read summary are covered through the UI;
// this checks the shared fold directly.
func TestHeadTailKeepsBothEnds(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString("line\n")
	}
	b.WriteString("last")
	got := HeadTail(b.String(), 2, 3)
	if len(got) != 6 || got[2] != "… +26 lines" || got[len(got)-1] != "last" {
		t.Fatalf("headTail = %q", got)
	}
	// Too short to be worth folding: returned whole.
	if got := HeadTail("a\nb\nc", 2, 3); len(got) != 3 {
		t.Fatalf("short input folded: %q", got)
	}
}
