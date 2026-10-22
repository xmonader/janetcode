package tui

import "testing"

// Only the lines before the first hunk are a file header: a removed line
// whose text starts with "-- " is drawn as "--- ..." and is content.
func TestPreviewLinesKeepsRemovedDashLines(t *testing.T) {
	diff := "--- a/q.sql\n+++ b/q.sql\n@@ -1,2 +1,2 @@\n--- a comment\n+-- b comment\n ctx\n"
	if got := previewLines(diff); got != 4 {
		t.Errorf("previewLines = %d, want 4 (hunk header, removed, added, context)", got)
	}
	if _, ok := parseUnifiedDiff(diff); !ok {
		t.Fatal("not parsed as a diff")
	}
	hunks, _ := parseUnifiedDiff(diff)
	if n := len(hunks[0].lines); n != 3 {
		t.Errorf("hunk has %d lines, want 3", n)
	}
}
