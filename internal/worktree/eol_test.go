package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A scratch checkout holds the committed bytes plus the patch, whatever
// core.autocrlf says: under Git for Windows' default (true) the checkout
// came out CRLF and the patch, made against LF, then failed to apply or
// left a mixed file.
func TestDetachedIsByteExactUnderAutocrlf(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	gitOut(t, repo, "config", "core.autocrlf", "true")
	base := gitOut(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\npatched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch := []byte(gitOut(t, repo, "diff", "--binary") + "\n")
	gitOut(t, repo, "checkout", "--", "a.txt")

	dir, cleanup, err := Detached(ctx, "eol", repo, base, patch)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "one\npatched\n" {
		t.Fatalf("a.txt = %q, want %q", got, "one\npatched\n")
	}
}
