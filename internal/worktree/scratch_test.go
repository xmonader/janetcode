package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/snapshot"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(context.Background(), dir, nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The checkout starts at base with the patch on top, never touches the
// repository's own tree, and cleanup leaves neither a directory nor a
// registration behind.
func TestDetachedAppliesPatchAndCleansUp(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	base := gitOut(t, repo, "rev-parse", "HEAD")

	// A patch made by editing the real tree, then undoing the edit.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\npatched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := gitx.RunBytes(ctx, repo, nil, "diff", "--binary")
	if err != nil {
		t.Fatal(err)
	}
	gitOut(t, repo, "checkout", "--", "a.txt")

	dir, cleanup, err := Detached(ctx, "test", repo, base, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(dir), "janetcode-test-") {
		t.Fatalf("dir = %s, want it named after the label", dir)
	}
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "one\npatched\n" {
		t.Fatalf("checkout a.txt = %q, want the patch applied", got)
	}
	if got := readFile(t, filepath.Join(repo, "a.txt")); got != "one\n" {
		t.Fatalf("repo a.txt = %q, the real tree was touched", got)
	}

	cleanup()
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("checkout still on disk after cleanup")
	}
	if list := gitOut(t, repo, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("git worktree list after cleanup:\n%s\nwant only the main worktree", list)
	}
}

// A cleanup after the checkout was deleted by hand still drops git's
// registration of it.
func TestDetachedCleanupAfterManualDelete(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	dir, cleanup, err := Detached(ctx, "gone", repo, "HEAD", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	cleanup()
	if list := gitOut(t, repo, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("git worktree list after cleanup:\n%s\nwant only the main worktree", list)
	}
}

func TestDetachedMissingBase(t *testing.T) {
	repo := initRepo(t)
	missing := strings.Repeat("a", 40)
	_, _, err := Detached(context.Background(), "x", repo, missing, nil)
	var mb *MissingBaseError
	if !errors.As(err, &mb) {
		t.Fatalf("err = %v, want a *MissingBaseError", err)
	}
	if want := "base " + missing + " is not in this repository: "; !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("err = %q, want it to start %q", err, want)
	}
}

func TestDetachedBadPatchCleansUp(t *testing.T) {
	repo := initRepo(t)
	bad := []byte("diff --git a/nope.txt b/nope.txt\n--- a/nope.txt\n+++ b/nope.txt\n@@ -1 +1 @@\n-x\n+y\n")
	if _, _, err := Detached(context.Background(), "bad", repo, "HEAD", bad); err == nil || !strings.HasPrefix(err.Error(), "git apply: ") {
		t.Fatalf("err = %v, want a git apply error", err)
	}
	if list := gitOut(t, repo, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("a failed Detached left a worktree registered:\n%s", list)
	}
}

// AtStart reproduces a session's start state: the committed tree plus
// whatever was uncommitted and untracked when the snapshot was taken.
func TestAtStartMatchesSnapshot(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\nedited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sr, err := snapshot.Open(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := sr.Take(ctx, "start")
	if err != nil {
		t.Fatal(err)
	}

	dir, cleanup, err := AtStart(ctx, "whatif", repo, snap.Head, snap.Commit)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := readFile(t, filepath.Join(dir, "a.txt")); got != "one\nedited\n" {
		t.Fatalf("a.txt = %q, want the uncommitted edit", got)
	}
	if got := readFile(t, filepath.Join(dir, "new.txt")); got != "untracked\n" {
		t.Fatalf("new.txt = %q, want the untracked file", got)
	}
}

// A session recorded before its repository had a commit replays into a
// git-initialised scratch directory holding the whole snapshot.
func TestAtStartUnbornRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	repo := t.TempDir()
	gitOut(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sr, err := snapshot.Open(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := sr.Take(ctx, "unborn start")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Head != "" {
		t.Fatalf("Head = %q, want empty on an unborn branch", snap.Head)
	}
	dir, cleanup, err := AtStart(ctx, "whatif", repo, snap.Head, snap.Commit)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got := readFile(t, filepath.Join(dir, "main.go")); got != "package main\n" {
		t.Fatalf("main.go = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("scratch dir is not git-initialised: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("scratch dir still on disk after cleanup")
	}
}

// A start commit missing from the repository is refused with the fix
// named, not a raw git error, and without eval keep's --self-contained
// advice.
func TestAtStartMissingBase(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	sr, err := snapshot.Open(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := sr.Take(ctx, "start")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = AtStart(ctx, "whatif", repo, strings.Repeat("a", 40), snap.Commit)
	if err == nil || !strings.Contains(err.Error(), "is not in this repository") || !strings.Contains(err.Error(), "fetch it") {
		t.Fatalf("err = %v, want the missing commit named with the fix", err)
	}
	if strings.Contains(err.Error(), "--self-contained") {
		t.Fatalf("err = %v, want no --self-contained advice", err)
	}
}
