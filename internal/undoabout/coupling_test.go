package undoabout

import (
	"github.com/xmonader/janetcode/internal/change"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeChange puts a change's After on disk and returns the change, as the
// session would have recorded it.
func writeChange(t *testing.T, seq int, path, before, after string) change.Change {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(after), 0o644); err != nil {
		t.Fatal(err)
	}
	return change.Change{Seq: seq, Path: path, Existed: true, Before: []byte(before), After: []byte(after)}
}

// A real /undo about warned eleven times that a kept test in another
// package "uses x89", "uses Mixed", "uses UTF": hex escapes, number
// fragments and words from comments and strings. None of those is a
// reference to anything, and a file in another directory cannot use an
// unexported name at all.
func TestCouplingIgnoresCommentsStringsNumbersAndOtherPackages(t *testing.T) {
	root := t.TempDir()
	undone := writeChange(t, 1, filepath.Join(root, "internal", "textfmt", "clip.go"),
		"package textfmt\n\nfunc Clip(s string) string { return s }\n",
		"package textfmt\n\n// Mixed UTF-8 input is clipped per rune.\nfunc Clip(s string) string { return s }\n\nconst pngMagic = \"\\x89PNG\"\nconst mask = 0x89ab\n\nfunc widthOf(s string) int { return len(s) }\n")
	kept := writeChange(t, 2, filepath.Join(root, "internal", "tui", "status_test.go"),
		"package tui\n\nimport \"testing\"\n\nfunc TestStatus(t *testing.T) {}\n",
		"package tui\n\nimport \"testing\"\n\nfunc TestStatus(t *testing.T) {\n\t// Mixed UTF-8 widths\n\tin := \"\\x89PNG\" /* UTF */\n\t_ = 0x89ab\n\twidthOf := len(in)\n\t_ = widthOf\n}\n")
	if w := detectCoupling(root, Selection{Undo: []change.Change{undone}, Keep: []change.Change{kept}}); len(w) != 0 {
		t.Fatalf("warnings = %q, want none", w)
	}
	// The same shape in one package, where the name is really shared, is
	// still caught — and only for the code, not the comment.
	same := writeChange(t, 3, filepath.Join(root, "internal", "textfmt", "clip_test.go"),
		"package textfmt\n\nimport \"testing\"\n\nfunc TestClip(t *testing.T) {}\n",
		"package textfmt\n\nimport \"testing\"\n\nfunc TestClip(t *testing.T) {\n\t// Mixed UTF\n\t_ = widthOf(\"\\x89\")\n}\n")
	w := detectCoupling(root, Selection{Undo: []change.Change{undone}, Keep: []change.Change{same}})
	if len(w) != 1 || !strings.Contains(w[0], "uses widthOf") {
		t.Fatalf("warnings = %q, want exactly the widthOf coupling", w)
	}
}
