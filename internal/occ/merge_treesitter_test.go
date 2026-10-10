//go:build treesitter

package occ

import (
	"strings"
	"testing"
)

const pyBase = "def a():\n    return 1\n\n\ndef b():\n    return 2\n"

// With a grammar compiled in, a Python file merges by declaration as Go
// does: adjacent-looking edits in two functions commute, two edits in one
// function do not, and the clash is named by the function.
func TestMergePythonByDeclaration(t *testing.T) {
	ours := strings.Replace(pyBase, "return 1", "return 10", 1)
	theirs := strings.Replace(pyBase, "return 2", "return 20", 1)
	got, why := Merge("m.py", []byte(pyBase), []byte(ours), []byte(theirs))
	if why != "" || !strings.Contains(string(got), "return 10") || !strings.Contains(string(got), "return 20") {
		t.Fatalf("why=%q got:\n%s", why, got)
	}
	ours = strings.Replace(pyBase, "def b():\n", "def b():\n    x = 0\n", 1)
	if _, why := Merge("m.py", []byte(pyBase), []byte(ours), []byte(theirs)); !strings.Contains(why, "both sides changed b") {
		t.Fatalf("why = %q", why)
	}
}
