package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An editing subagent's checkout holds the repository's bytes, and its
// patch carries only what the subagent changed, under Git for Windows'
// default core.autocrlf=true too. Before, the checkout came out CRLF and
// the patch rewrote the line endings of every file the subagent touched.
func TestSubagentWorktreeIsByteExactUnderAutocrlf(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "init")
	git("config", "core.autocrlf", "true")

	ctx := context.Background()
	w, err := newWorktree(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.remove(ctx)
	b, err := os.ReadFile(filepath.Join(w.Dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "one\ntwo\n" {
		t.Fatalf("checkout a.txt = %q, want the committed bytes", b)
	}
	if err := os.WriteFile(filepath.Join(w.Dir, "a.txt"), []byte("one\n2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, _, err := w.diff(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(patch, "\r") || !strings.Contains(patch, "+2\n") {
		t.Fatalf("patch should change one line with LF endings:\n%q", patch)
	}
}
