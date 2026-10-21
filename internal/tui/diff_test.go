package tui

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
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
	out := plain(renderDiff(defaultStyles(), hunks, 70, 40, true))
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

// A removed line that itself starts "-- " (an SQL or Lua comment) is
// "--- …" in the diff, and an added "++ " line is "+++ …": both were read
// as file headers and dropped, so an edit's preview hid a line it removed.
func TestDiffKeepsLinesThatLookLikeFileHeaders(t *testing.T) {
	d := "--- a.sql\n+++ a.sql\n@@ -1,3 +1,3 @@\n select 1;\n--- drop the old table\n+++ counter\n select 2;\n"
	hunks, ok := parseUnifiedDiff(d)
	if !ok || len(hunks) != 1 {
		t.Fatalf("parse: ok=%v hunks=%d", ok, len(hunks))
	}
	out := plain(renderDiff(defaultStyles(), hunks, 70, 40, true))
	for _, want := range []string{"2 │ --- drop the old table", "2 │ +++ counter"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	// A second file's headers after a hunk has run its course are still
	// headers, not lines.
	two := "--- a\n+++ a\n@@ -1 +1 @@\n-x\n+y\n--- b\n+++ b\n@@ -5,1 +5,1 @@\n-p\n+q\n"
	hunks, ok = parseUnifiedDiff(two)
	if !ok || len(hunks) != 2 || len(hunks[0].lines) != 2 {
		t.Fatalf("two files: ok=%v hunks=%+v", ok, hunks)
	}
}

// TestNonDiffPreviewStillRenders: a preview that is not a unified diff
// falls back to plain text.
func TestNonDiffPreviewStillRenders(t *testing.T) {
	if _, ok := parseUnifiedDiff("would delete 3 files\n  a.txt\n"); ok {
		t.Fatal("plain text parsed as a diff")
	}
	if out := plain(renderPreview(defaultStyles(), "would delete 3 files\n  a.txt\n", 60, 10, true)); !strings.Contains(out, "would delete 3 files") {
		t.Fatalf("plain preview lost:\n%s", out)
	}
}

// A changed line is a tinted band across the width, the changed words in a
// stronger tint rather than inverse video; a context line has no band.
func TestDiffLinesAreTintedBands(t *testing.T) {
	hunks, ok := parseUnifiedDiff("@@ -1,3 +1,3 @@\n ctx\n-\treturn a + b\n+\treturn a - b\n")
	if !ok {
		t.Fatal("not parsed")
	}
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)
	sty := defaultStyles()
	lines := strings.Split(renderDiff(sty, hunks, 60, 50, false), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	if strings.Contains(lines[0], "48;") {
		t.Fatalf("context line tinted: %q", lines[0])
	}
	for _, l := range lines[1:] {
		if !strings.Contains(l, "48;") || strings.Contains(l, "\x1b[7m") || strings.Contains(l, ";7m") {
			t.Fatalf("changed line not a tinted band: %q", l)
		}
		if w := lipgloss.Width(l); w != 58 {
			t.Fatalf("band %d wide, want the width: %q", w, l)
		}
	}
	// A palette of ANSI indices cannot be tinted: inverse video stays.
	p := defaultPalette
	p.diffAdd = lipgloss.AdaptiveColor{Light: "2", Dark: "2"}
	if s := buildStyles(p); s.diffTint {
		t.Fatal("tinted an ANSI palette")
	}
}

// TestNarrowDiffShowsTheChangedWord: a one-word change past the right
// edge of a narrow window was cut off with the rest of the row, so the
// removed and added lines read the same and the change was not on screen.
func TestNarrowDiffShowsTheChangedWord(t *testing.T) {
	d := "@@ -7,1 +7,1 @@\n-\t\tfmt.Println(\"hello world this is a long line of text that goes on and on and HELLO\")\n+\t\tfmt.Println(\"hello world this is a long line of text that goes on and on and GOODBYE\")\n"
	hunks, ok := parseUnifiedDiff(d)
	if !ok {
		t.Fatal("parse")
	}
	out := strings.Split(plain(renderDiff(defaultStyles(), hunks, 40, 40, true)), "\n")
	if len(out) != 2 || !strings.Contains(out[0], "HELLO") || !strings.Contains(out[1], "GOODBYE") {
		t.Fatalf("the changed words are not on screen:\n%s", strings.Join(out, "\n"))
	}
	if strings.Index(out[0], "HELLO") != strings.Index(out[1], "GOODBYE") {
		t.Fatalf("the pair fell out of line:\n%s", strings.Join(out, "\n"))
	}
	for _, l := range out {
		if lipgloss.Width(l) > 38 {
			t.Fatalf("row wider than the window: %q", l)
		}
	}
	// A change that fits is drawn from the start of the line, as before.
	short := "@@ -1,1 +1,1 @@\n-a := 1\n+a := 2\n"
	hunks, _ = parseUnifiedDiff(short)
	if got := plain(renderDiff(defaultStyles(), hunks, 40, 40, true)); strings.Contains(got, "…") {
		t.Fatalf("a short change was shifted: %q", got)
	}
}

// TestDiffGutterFitsOldLineNumbers:the gutter was sized from the new
// file's line numbers only, but a removed line is numbered in the old
// one. After a hunk that deleted most of a file, the next hunk's old
// numbers had more digits than the gutter and pushed their rows out of
// line with the rest.
func TestDiffGutterFitsOldLineNumbers(t *testing.T) {
	var b strings.Builder
	b.WriteString("@@ -1,90 +1,0 @@\n")
	for i := 0; i < 90; i++ {
		b.WriteString("-gone\n")
	}
	b.WriteString("@@ -120,1 +30,1 @@\n-old\n+new\n")
	hunks, ok := parseUnifiedDiff(b.String())
	if !ok {
		t.Fatal("parse")
	}
	col := -1
	for _, l := range strings.Split(plain(renderDiff(defaultStyles(), hunks, 70, 400, true)), "\n") {
		i := strings.Index(l, "│")
		if i < 0 {
			continue
		}
		if col < 0 {
			col = i
		}
		if i != col {
			t.Fatalf("gutter out of line at %q (column %d, others %d)", l, i, col)
		}
	}
}
