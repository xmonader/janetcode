package change

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/diff"
)

// Two changes far enough apart to make two hunks each get hunkContext
// lines of context on both sides, as git diff gives them. The first hunk
// used to end one line short: the equal line that ended the scan was
// counted in the run of context but never taken into the hunk.
func TestMakeHunksKeepsFullTrailingContext(t *testing.T) {
	var before, after []string
	for i := 1; i <= 20; i++ {
		l := strings.Repeat("x", i) + "\n"
		before = append(before, l)
		if i == 2 || i == 15 {
			l = "changed\n"
		}
		after = append(after, l)
	}
	hunks := MakeHunks(DiffLines(before, after))
	if len(hunks) != 2 {
		t.Fatalf("got %d hunks, want 2", len(hunks))
	}
	// Line 2 changed: one line before it, three after.
	if got := hunks[0].Header(); got != "@@ -1,5 +1,5 @@" {
		t.Fatalf("first hunk = %s, want @@ -1,5 +1,5 @@", got)
	}
	// Line 15 changed: three lines on each side.
	if got := hunks[1].Header(); got != "@@ -12,7 +12,7 @@" {
		t.Fatalf("second hunk = %s, want @@ -12,7 +12,7 @@", got)
	}
}

// Changes six lines apart share one hunk, as in git: their context
// overlaps.
func TestMakeHunksMergesCloseChanges(t *testing.T) {
	before := diff.SplitLinesKeepEOL("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n")
	after := diff.SplitLinesKeepEOL("A\nb\nc\nd\ne\nf\ng\nH\ni\nj\n")
	if hunks := MakeHunks(DiffLines(before, after)); len(hunks) != 1 {
		t.Fatalf("got %d hunks, want 1", len(hunks))
	}
}
