// Package worktree runs a session in its own git worktree: the agent
// works on a branch in a separate checkout, so the user's tree is never
// touched, two sessions can run on one repository at once, and the result
// is an ordinary branch to review and merge rather than a patch to apply.
package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Tree is one session's checkout.
type Tree struct {
	// Path is the checkout the session works in.
	Path string
	// Branch is what it is committing to.
	Branch string
	// Repo is the repository the worktree belongs to.
	Repo string
	// Created is false when an existing worktree for the branch was reused.
	Created bool
}

// Open returns the worktree for branch, making it if it is not there. The
// checkout lives inside the repository's git directory, which nothing
// scans and git already ignores, so the agent cannot wander into it and a
// build cannot pick it up.
//
// Nothing removes the checkout when the session ends, on purpose: the
// branch is the user's to review and merge, and naming it again continues
// in the same checkout (docs/manual/interface.md, "A whole session in a
// worktree"). `git worktree remove` is how a user discards one.
func Open(ctx context.Context, repo, branch string) (*Tree, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil, fmt.Errorf("worktree: a branch name is required")
	}
	if strings.ContainsAny(branch, " \t\\:?*[") || strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("worktree: %q is not a usable branch name", branch)
	}
	if _, err := gitx.Run(ctx, repo, nil, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("worktree: %s is not a git repository", repo)
	}
	common, err := gitx.Run(ctx, repo, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		// Older git without --path-format: fall back to the plain form.
		if common, err = gitx.Run(ctx, repo, nil, "rev-parse", "--git-common-dir"); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(common) {
			common = filepath.Join(repo, common)
		}
	}
	path := filepath.Join(common, "janetcode-worktrees", sanitize(branch))

	// A worktree already registered for this path is reused, so running
	// the same branch twice continues the work instead of failing — but
	// only when git still has it and the checkout is really there. A
	// registration whose directory has been deleted is stale, and
	// pinning a session to it would run the whole session against a
	// directory that is not a checkout of anything.
	if existing, err := worktreeAt(ctx, repo, path); err == nil && existing {
		t := &Tree{Path: path, Branch: branch, Repo: repo}
		verr := t.Validate(ctx)
		if verr == nil {
			return t, nil
		}
		// Only a registration whose directory has gone is pruned. A
		// checkout that is there but does not satisfy Validate — most
		// likely another branch whose name maps to this same directory —
		// may hold work nobody has merged, so it is reported, not deleted.
		if _, serr := os.Stat(path); serr == nil {
			return nil, verr
		}
		if _, err := gitx.Run(ctx, repo, nil, "worktree", "prune"); err != nil {
			return nil, fmt.Errorf("worktree: %s is registered but missing, and it could not be pruned: %w", path, err)
		}
		_ = os.RemoveAll(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	args := []string{"worktree", "add"}
	if branchExists(ctx, repo, branch) {
		args = append(args, path, branch)
	} else {
		args = append(args, "-b", branch, path)
	}
	if _, err := gitx.Run(ctx, repo, nil, args...); err != nil {
		return nil, gitx.Wrapf(err, "worktree: git %s", strings.Join(args, " "))
	}
	t := &Tree{Path: path, Branch: branch, Repo: repo, Created: true}
	if err := t.Validate(ctx); err != nil {
		return nil, err
	}
	return t, nil
}

// Validate checks the checkout against git's own registry before a
// session is pinned to it: the path must be a directory git lists as a
// worktree of this repository. Everything the session does — its tools,
// its map, its language servers — is rooted there, so a path git does
// not know about is a session working somewhere nobody can merge from.
func (t *Tree) Validate(ctx context.Context) error {
	info, err := os.Stat(t.Path)
	if err != nil {
		return fmt.Errorf("worktree: %s: %w", t.Path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("worktree: %s is not a directory", t.Path)
	}
	listed, err := worktreeAt(ctx, t.Repo, t.Path)
	if err != nil {
		return fmt.Errorf("worktree: git worktree list: %w", err)
	}
	if !listed {
		return fmt.Errorf("worktree: git does not list %s as a worktree of %s", t.Path, t.Repo)
	}
	// The directory is named after the branch with its separators folded
	// out (see sanitize), so "feat/x" and "feat-x" want the same one. A
	// session pinned to a checkout of the other branch would commit its
	// whole run to the wrong place, so the branch is checked, not assumed.
	on, err := gitx.Run(ctx, t.Path, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("worktree: %s: %w", t.Path, err)
	}
	if on != t.Branch {
		return fmt.Errorf("worktree: %s is checked out at %s, not %s; another branch name maps to the same directory — rename one of them or remove that worktree", t.Path, on, t.Branch)
	}
	return nil
}

// Summary describes what the session left behind, for the message printed
// when it ends: how many commits are on the branch and whether anything is
// still uncommitted.
type Summary struct {
	Commits int
	Dirty   bool
	Base    string
}

// Summarize compares the branch with the commit it started from.
func (t *Tree) Summarize(ctx context.Context) Summary {
	s := Summary{}
	base, err := gitx.Run(ctx, t.Repo, nil, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && base != t.Branch {
		s.Base = base
	}
	if s.Base != "" {
		if out, err := gitx.Run(ctx, t.Path, nil, "rev-list", "--count", s.Base+".."+t.Branch); err == nil {
			fmt.Sscanf(out, "%d", &s.Commits)
		}
	}
	if out, err := gitx.Run(ctx, t.Path, nil, "status", "--porcelain"); err == nil {
		s.Dirty = strings.TrimSpace(out) != ""
	}
	return s
}

// String is the one-line note shown to the user when the session ends.
func (s Summary) String() string {
	switch {
	case s.Commits == 0 && !s.Dirty:
		return "nothing was committed"
	case s.Commits == 0:
		return "uncommitted changes only"
	case s.Dirty:
		return fmt.Sprintf("%s, plus uncommitted changes", textfmt.Plural(s.Commits, "commit"))
	default:
		return textfmt.Plural(s.Commits, "commit")
	}
}

// worktreeAt reports whether git already has a worktree checked out at path.
func worktreeAt(ctx context.Context, repo, path string) (bool, error) {
	out, err := gitx.Run(ctx, repo, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want := resolve(path)
	for _, line := range strings.Split(out, "\n") {
		listed, ok := strings.CutPrefix(strings.TrimSpace(line), "worktree ")
		if ok && resolve(listed) == want {
			return true, nil
		}
	}
	return false, nil
}

// resolve is a path as git would print it: cleaned, and with symlinks
// followed where they can be (macOS reports /private/var for /var).
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(p)
}

func branchExists(ctx context.Context, repo, branch string) bool {
	_, err := gitx.Run(ctx, repo, nil, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// sanitize makes a branch name safe as one path segment; a branch with
// slashes would otherwise nest directories under the worktree root.
func sanitize(branch string) string {
	return strings.NewReplacer("/", "-", string(filepath.Separator), "-").Replace(branch)
}
