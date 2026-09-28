package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The Python check was `python3 -m py_compile`, which does not only
// parse: it writes the compiled file into __pycache__ beside the source,
// so every edit to a .py file left a directory in the user's tree that
// nobody asked for — in git status, in the undo snapshot, in the diff.
func TestPythonSyntaxCheckWritesNothingToTheTree(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good.py")
	if err := os.WriteFile(good, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if note := syntaxFeedback(context.Background(), good); note != "" {
		t.Fatalf("a valid file failed the check: %s", note)
	}
	if _, err := os.Stat(filepath.Join(dir, "__pycache__")); err == nil {
		t.Fatal("the syntax check wrote __pycache__ into the tree")
	}
	bad := filepath.Join(dir, "bad.py")
	if err := os.WriteFile(bad, []byte("def f(:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if note := syntaxFeedback(context.Background(), bad); !strings.Contains(note, "SyntaxError") {
		t.Fatalf("a syntax error was not reported: %q", note)
	}
}
