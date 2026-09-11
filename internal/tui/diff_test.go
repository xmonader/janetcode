package tui

import (
	"strings"
	"testing"
)

// TestDiffGutterAndElision: a diff is drawn with line numbers from the
// hunk headers, at most three lines of context, and a count of the
// unchanged lines between two hunks.
func TestDiffGutterAndElision(t *testing.T) {
	d := "--- a.go\n+++ a.go\n@@ -10,7 +10,7 @@\n one\n two\n three\n four\n-\treturn a + b\n+\treturn a - b\n five\n@@ -60,3 +60,4 @@\n x\n+added\n y\n"
	hunks, ok := parseUnifiedDiff(d)
	if !ok || len(hunks) != 2 {
		t.Fatalf("parse: ok=%v hunks=%d", ok, len(hunks))
	}
	out := plain(renderDiff(hunks, 70, 40, true))
	if strings.Contains(out, "one") {
		t.Fatalf("context was not trimmed to three lines:\n%s", out)
	}
	for _, want := range []string{"11 │  two", "14 │ -", "14 │ +", "… 44 unchanged lines", "61 │ +added"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	// The gutter is as wide as the largest line number, not wider.
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "│") && !strings.HasPrefix(l, "  ") {
			t.Fatalf("gutter not padded: %q", l)
		}
	}
}

// TestWordLevelHighlight: for one line replaced by one line, only the
// tokens that differ are picked out, and never the indentation.
func TestWordLevelHighlight(t *testing.T) {
	from, to := changedSpan("\treturn a + b", "\treturn a - b")
	if got := "\treturn a + b"[from:to]; got != "+" {
		t.Fatalf("changed span = %q (%d..%d)", got, from, to)
	}
	// A rename takes the whole token, not the letters that differ.
	from, to = changedSpan("  err := doThing(x)", "  err := doOther(x)")
	if got := "  err := doThing(x)"[from:to]; got != "doThing" {
		t.Fatalf("token span = %q", got)
	}
	// A line that only gained indentation highlights nothing before its text.
	from, _ = changedSpan("\t\tx := 1", "\tx := 1")
	if from < 2 {
		t.Fatalf("indentation was highlighted (from=%d)", from)
	}
	// Identical lines have an empty span.
	if a, b := changedSpan("same", "same"); a < b {
		t.Fatalf("identical lines have a span %d..%d", a, b)
	}
}

// TestNonDiffPreviewStillRenders: a preview that is not a unified diff
// falls back to plain text.
func TestNonDiffPreviewStillRenders(t *testing.T) {
	if _, ok := parseUnifiedDiff("would delete 3 files\n  a.txt\n"); ok {
		t.Fatal("plain text parsed as a diff")
	}
	if out := plain(renderPreview("would delete 3 files\n  a.txt\n", 60, 10, true)); !strings.Contains(out, "would delete 3 files") {
		t.Fatalf("plain preview lost:\n%s", out)
	}
}
