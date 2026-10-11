package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
)

// TestNoNewlineMarkerIsNotALine: "\ No newline at end of file" (which
// change.Hunk.String writes after a last line with no newline) is a note
// about the line above it, not a line of the file. It was drawn as a
// numbered context line, and so pushed every number after it down by one.
func TestNoNewlineMarkerIsNotALine(t *testing.T) {
	h := change.Hunk{OldStart: 1, OldCount: 3, NewStart: 1, NewCount: 3,
		Lines: []string{" a\n", " b\n", "-\treturn x", "+\treturn y\n"}}
	d := "--- f.go\n+++ f.go\n" + h.String()
	if !strings.Contains(d, `\ No newline at end of file`) {
		t.Fatalf("the hunk carries no marker to test with:\n%s", d)
	}
	hunks, ok := parseUnifiedDiff(d)
	if !ok {
		t.Fatalf("not parsed as a diff:\n%s", d)
	}
	sty := defaultStyles()
	raw := renderDiff(sty, hunks, 70, 40, true)
	out := plain(raw)
	for _, want := range []string{"1 │  a", "3 │ -", "3 │ +", "return y"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	var marker string
	for _, l := range strings.Split(raw, "\n") {
		if strings.Contains(plain(l), "No newline at end of file") {
			marker = l
		}
	}
	if marker == "" {
		t.Fatalf("the marker was dropped:\n%s", out)
	}
	if p := plain(marker); strings.ContainsAny(p, "0123456789") {
		t.Fatalf("the marker took a line number: %q\n%s", p, out)
	}
	if want := sty.faint.Render(`\ No newline at end of file`); !strings.Contains(marker, want) {
		t.Fatalf("the marker should be faint: %q", marker)
	}
	// The pair around it is still one line replaced by one line, so the
	// changed word is picked out.
	if d, a, ok := hunks[0].trimContext().changePair(); !ok || d >= a {
		t.Fatalf("the marker broke the change pair: %d %d %v", d, a, ok)
	}
}
