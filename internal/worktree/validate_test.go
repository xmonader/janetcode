package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Everything a session does is rooted in its checkout, so the path is
// checked against git's own registry before the session is pinned to it.
func TestValidateChecksGitsOwnList(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	tree, err := Open(ctx, repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.Validate(ctx); err != nil {
		t.Fatalf("a freshly created worktree does not validate: %v", err)
	}
	// A directory git knows nothing about is not a worktree.
	stray := &Tree{Path: t.TempDir(), Branch: "feature", Repo: repo}
	if err := stray.Validate(ctx); err == nil {
		t.Fatal("a path git does not list was accepted")
	}
	// Neither is a path that is not there at all.
	gone := &Tree{Path: filepath.Join(t.TempDir(), "nope"), Branch: "feature", Repo: repo}
	if err := gone.Validate(ctx); err == nil {
		t.Fatal("a missing path was accepted")
	}
}

// A registration whose checkout has been deleted is stale: reopening it
// must make a real checkout rather than hand the session a directory
// that is not one.
func TestOpenReplacesAStaleRegistration(t *testing.T) {
	repo := initRepo(t)
	ctx := context.Background()
	tree, err := Open(ctx, repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(tree.Path); err != nil {
		t.Fatal(err)
	}
	again, err := Open(ctx, repo, "feature")
	if err != nil {
		t.Fatalf("reopen after the checkout was deleted: %v", err)
	}
	if err := again.Validate(ctx); err != nil {
		t.Fatalf("the reopened worktree does not validate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(again.Path, "a.txt")); err != nil {
		t.Fatalf("the checkout was not really made: %v", err)
	}
}
