package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
)

// This file is the throwaway side of the package: checkouts that exist for
// one run (an eval task, a --what-if replay, a mutation-testing pass) and
// are removed afterwards, as opposed to Open's named branch, which is kept
// for the user to merge.

// MissingBaseError is Detached's error when base is not a commit in the
// repository. Callers add their own advice (fetch it, re-keep a task with
// --self-contained) by matching it with errors.As.
type MissingBaseError struct {
	Base   string
	Detail string // git's own explanation
}

func (e *MissingBaseError) Error() string {
	return fmt.Sprintf("base %s is not in this repository: %s", e.Base, e.Detail)
}

// Detached checks repo out at the commit base, detached, into a fresh
// temporary directory and applies patch (when non-empty) on top. label
// names the directory (janetcode-<label>-*) so a checkout left behind on
// purpose, such as an eval run with --keep, says what made it. cleanup
// removes the checkout and its registration with git; it is safe to call
// once the context that made the checkout has been cancelled.
func Detached(ctx context.Context, label, repo, base string, patch []byte) (dir string, cleanup func(), err error) {
	if err := gitx.CommitExists(ctx, repo, base); err != nil {
		return "", nil, &MissingBaseError{Base: base, Detail: gitx.Detail(err)}
	}
	dir, err = os.MkdirTemp("", tempPattern(label))
	if err != nil {
		return "", nil, err
	}
	// `git worktree add` refuses to check out into a path that already
	// exists, even an empty one MkdirTemp just made. Until the checkout
	// succeeds there is no cleanup for the caller to run, so both failure
	// paths below remove the directory themselves: `git worktree add` can
	// fail after it has begun populating the path (a checkout that runs
	// out of disk, a filter that errors), and nothing else would ever
	// reclaim what it left there.
	if err := os.Remove(dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	raw := gitx.ByteExact(ctx, repo)
	if _, err := gitx.Run(ctx, repo, raw, "worktree", "add", "--detach", "-q", dir, base); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, gitx.Wrapf(err, "git worktree add %s", base)
	}
	cleanup = func() {
		bg := context.Background()
		if _, err := gitx.Run(bg, repo, nil, "worktree", "remove", "--force", dir); err != nil {
			// The directory may already be gone; drop the stale
			// registration so git does not keep listing it.
			_ = os.RemoveAll(dir)
			_, _ = gitx.Run(bg, repo, nil, "worktree", "prune")
		}
		_ = os.RemoveAll(dir)
	}
	if err := applyPatch(ctx, dir, patch); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// Scratch is Detached for when there is no commit to start from: a plain
// temporary directory, git-initialised when git is available, with patch
// applied on top. cleanup removes it.
func Scratch(ctx context.Context, label string, patch []byte) (dir string, cleanup func(), err error) {
	dir, err = os.MkdirTemp("", tempPattern(label))
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.RemoveAll(dir) }
	if _, lerr := exec.LookPath("git"); lerr == nil {
		_, _ = gitx.Run(ctx, dir, nil, "init", "-q")
	}
	if err := applyPatch(ctx, dir, patch); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// AtStart reproduces a session's recorded start state (session.StartState:
// head is the commit HEAD pointed to, empty on an unborn branch; snap is
// the snapshot commit of the whole work tree) as a scratch checkout:
// repo's tree at head with whatever was uncommitted at that moment applied
// on top. A session recorded before its repository had any commit gets
// Scratch with the whole snapshot applied as a patch from the empty tree.
// --what-if uses it to put a session's tree back where its first turn
// found it.
func AtStart(ctx context.Context, label, repo, head, snap string) (dir string, cleanup func(), err error) {
	if snap == "" {
		return "", nil, fmt.Errorf("session has no recorded start state")
	}
	if repo == "" {
		return "", nil, fmt.Errorf("no repository to replay the session's start state against")
	}
	if head == "" {
		patch, err := gitx.Diff(ctx, repo, "", snap)
		if err != nil {
			return "", nil, fmt.Errorf("diff session start: %w", err)
		}
		return Scratch(ctx, label, patch)
	}
	// Checked before diffing, not just by Detached: a missing head fails
	// `git diff` itself with a raw "bad object" error first.
	if err := gitx.CommitExists(ctx, repo, head); err != nil {
		missing := &MissingBaseError{Base: head, Detail: gitx.Detail(err)}
		return "", nil, fmt.Errorf("%w: fetch it before replaying this session", missing)
	}
	patch, err := gitx.Diff(ctx, repo, head, snap)
	if err != nil {
		return "", nil, fmt.Errorf("diff session start: %w", err)
	}
	return Detached(ctx, label, repo, head, patch)
}

// applyPatch applies patch in dir, doing nothing for an empty one.
func applyPatch(ctx context.Context, dir string, patch []byte) error {
	if len(bytes.TrimSpace(patch)) == 0 {
		return nil
	}
	// Byte-exact, as the checkout under it was: git apply converts line
	// endings on the files it writes the same way a checkout does. Only
	// the config overrides, not GIT_ATTR_SOURCE: git 2.43's apply
	// segfaults with it set.
	if err := gitx.ApplyEnv(ctx, dir, gitx.ByteExactEnv(""), patch); err != nil {
		return gitx.Wrapf(err, "git apply")
	}
	return nil
}

// tempPattern is the os.MkdirTemp pattern for label: letters, digits, '-'
// and '_' kept, everything else dropped.
func tempPattern(label string) string {
	var b strings.Builder
	for _, r := range label {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return "janetcode-" + b.String() + "-"
}
