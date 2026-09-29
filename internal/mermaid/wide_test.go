package mermaid

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A label in a wide script (CJK, emoji) takes two cells per character.
// Boxes were sized by rune count, so the row holding the label came out
// wider than the box's own border. Such a diagram is now shown as its
// source; so is one whose label carries a control character.
func TestLabelsThatAreNotOneCellPerRuneFallBack(t *testing.T) {
	for _, src := range []string{
		"flowchart TD\nA[日本語のラベル] --> B[ok]",
		"flowchart LR\nA[😀 start] --> B[ok]",
		"sequenceDiagram\nA->>B: 日本語",
		"sequenceDiagram\nparticipant A as 日本\nA->>B: hi",
		"flowchart TD\nA[é] --> B[ok]",
		"flowchart TD\nA[x\x1b[2Jy] --> B[ok]",
	} {
		if lines, ok := Render(src, 80); ok {
			t.Fatalf("%q was drawn:\n%s", src, strings.Join(lines, "\n"))
		}
	}
}

// Narrow non-ASCII still draws, and every row of a box is as wide on
// screen as its border — including a label clipped with "…".
func TestNarrowNonASCIILabelsStillDraw(t *testing.T) {
	src := "flowchart TD\nA[café naïve Ωμέγα] --> B[" + strings.Repeat("x", 60) + "]"
	lines, ok := Render(src, 80)
	if !ok {
		t.Fatal("a diagram of one-cell characters fell back to source")
	}
	if a, b := ansi.StringWidth(lines[0]), ansi.StringWidth(lines[1]); a != b {
		t.Fatalf("box rows differ in width (%d vs %d):\n%s", a, b, strings.Join(lines, "\n"))
	}
}
