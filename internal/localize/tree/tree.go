// Package tree is where internal/localize reruns tests: a throwaway git
// worktree of the repository as it stands, uncommitted changes included.
// It is a package of its own so the agent loop can use localize's types
// without importing the worktree machinery (see internal/archtest); the
// composition root hands the agent Run.
package tree

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/localize"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/worktree"
)

// Run is localize.Run in a throwaway worktree.
func Run(ctx context.Context, req localize.Request) localize.Report {
	req.Scratch = Scratch
	return localize.Run(ctx, req)
}

// Scratch checks the repository holding root out, as it stands, into a
// throwaway worktree, and returns root's directory inside it. cleanup
// removes the worktree and the snapshot it was made from.
func Scratch(ctx context.Context, root string) (dir string, cleanup func(), err error) {
	repo, err := snapshot.Open(ctx, root)
	if err != nil {
		return "", nil, err
	}
	snap, err := repo.Take(ctx, "localize base")
	if err != nil {
		return "", nil, err
	}
	// Dropping the snapshot has to work after ctx is cancelled — that is
	// when it matters most — without being able to hang forever.
	drop := func() {
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer dcancel()
		repo.Drop(dctx, snap)
	}
	// The project may be a subdirectory of the repository: its files and
	// the tests' directory are under the same prefix in the checkout.
	prefix, err := gitx.Run(ctx, root, nil, "rev-parse", "--show-prefix")
	if err != nil {
		drop()
		return "", nil, err
	}
	top, done, err := worktree.Detached(ctx, "localize", repo.Root, snap.Commit, nil)
	if err != nil {
		drop()
		return "", nil, err
	}
	return filepath.Join(top, filepath.FromSlash(strings.TrimSpace(prefix))), func() { done(); drop() }, nil
}
