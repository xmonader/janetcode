package mermaid

import (
	"strings"
	"testing"
)

// A label given on a node's second mention is clipped like one given on
// its first; it used to be stored whole, so the box outgrew the terminal
// and the diagram fell back to source for no reason.
func TestALabelGivenLaterIsClipped(t *testing.T) {
	d, err := Parse("flowchart TD\nA --> B\nB[" + strings.Repeat("word ", 30) + "]")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(d.Nodes[1].Label)); n > maxLabel {
		t.Fatalf("label is %d runes, over the %d cap", n, maxLabel)
	}
	d, err = Parse("sequenceDiagram\nA->>B: hi\nparticipant B as " + strings.Repeat("word ", 30))
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(d.Actors[1].Label)); n > maxLabel {
		t.Fatalf("actor label is %d runes, over the %d cap", n, maxLabel)
	}
}
