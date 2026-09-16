package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(t, dir, "first")
	return dir
}

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}

func TestOpenCreatesReusesAndIsolates(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	wt, err := Open(ctx, repo, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	if !wt.Created || wt.Branch != "feature/x" {
		t.Fatalf("tree = %+v", wt)
	}
	// The checkout lives inside .git, where nothing scanning the project
	// will find it.
	if !strings.Contains(wt.Path, ".git") || strings.Contains(filepath.Base(wt.Path), "/") {
		t.Fatalf("path = %q", wt.Path)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "a.txt")); err != nil {
		t.Fatalf("checkout is missing the repository's files: %v", err)
	}

	// Work in the worktree does not touch the real tree.
	if err := os.WriteFile(filepath.Join(wt.Path, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit(t, wt.Path, "add b")
	if _, err := os.Stat(filepath.Join(repo, "b.txt")); err == nil {
		t.Fatal("the worktree's file leaked into the real tree")
	}

	s := wt.Summarize(ctx)
	if s.Commits != 1 || s.Dirty || s.Base != "main" {
		t.Fatalf("summary = %+v", s)
	}
	if got := s.String(); got != "1 commit" {
		t.Fatalf("summary line = %q", got)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, "c.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := wt.Summarize(ctx).String(); got != "1 commit, plus uncommitted changes" {
		t.Fatalf("dirty summary = %q", got)
	}

	// Opening the same branch again continues in the same checkout.
	again, err := Open(ctx, repo, "feature/x")
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.Path != wt.Path {
		t.Fatalf("reopen = %+v, want the existing %s", again, wt.Path)
	}
}

func TestOpenRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	for _, bad := range []string{"", "  ", "with space", "-dash", "a:b"} {
		if _, err := Open(ctx, repo, bad); err == nil {
			t.Fatalf("branch %q accepted", bad)
		}
	}
	if _, err := Open(ctx, t.TempDir(), "x"); err == nil {
		t.Fatal("non-repository accepted")
	}
}

func TestOpenUsesAnExistingBranch(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	cmd := exec.Command("git", "branch", "already")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	wt, err := Open(ctx, repo, "already")
	if err != nil {
		t.Fatalf("existing branch: %v", err)
	}
	if !wt.Created {
		t.Fatal("a new checkout of an existing branch is still created")
	}
	if s := wt.Summarize(ctx); s.Commits != 0 || s.String() != "nothing was committed" {
		t.Fatalf("summary = %+v", s)
	}
}

// The checkout directory is named after the branch with its separators
// folded out, so "feat/x" and "feat-x" land on the same path. The second
// session must not be pinned to the first's checkout — it would commit
// its whole run to the other branch — and must not delete it either.
func TestCollidingBranchNamesAreRefusedNotReused(t *testing.T) {
	ctx := context.Background()
	repo := initRepo(t)
	first, err := Open(ctx, repo, "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "work.txt"), []byte("unmerged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := Open(ctx, repo, "feat-x")
	if err == nil {
		t.Fatalf("a colliding branch name was accepted: %+v", second)
	}
	if !strings.Contains(err.Error(), "feat/x") {
		t.Fatalf("the error should name the branch that is there: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(first.Path, "work.txt")); serr != nil {
		t.Fatalf("the existing checkout was destroyed: %v", serr)
	}
	// The original branch still opens.
	if _, err := Open(ctx, repo, "feat/x"); err != nil {
		t.Fatalf("reopening the original: %v", err)
	}
}
