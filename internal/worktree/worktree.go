// Package worktree runs a session in its own git worktree: the agent
// works on a branch in a separate checkout, so the user's tree is never
// touched, two sessions can run on one repository at once, and the result
// is an ordinary branch to review and merge rather than a patch to apply.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func Open(ctx context.Context, repo, branch string) (*Tree, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return nil, fmt.Errorf("worktree: a branch name is required")
	}
	if strings.ContainsAny(branch, " \t\\:?*[") || strings.HasPrefix(branch, "-") {
		return nil, fmt.Errorf("worktree: %q is not a usable branch name", branch)
	}
	if _, err := git(ctx, repo, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("worktree: %s is not a git repository", repo)
	}
	common, err := git(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		// Older git without --path-format: fall back to the plain form.
		if common, err = git(ctx, repo, "rev-parse", "--git-common-dir"); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(common) {
			common = filepath.Join(repo, common)
		}
	}
	path := filepath.Join(common, "janetcode-worktrees", sanitize(branch))

	// A worktree already registered for this path is reused, so running
	// the same branch twice continues the work instead of failing.
	if existing, err := worktreeAt(ctx, repo, path); err == nil && existing {
		return &Tree{Path: path, Branch: branch, Repo: repo}, nil
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
	if out, err := git(ctx, repo, args...); err != nil {
		return nil, fmt.Errorf("worktree: git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return &Tree{Path: path, Branch: branch, Repo: repo, Created: true}, nil
}

// Remove deletes the checkout. The branch and its commits stay.
func (t *Tree) Remove(ctx context.Context) error {
	_, err := git(ctx, t.Repo, "worktree", "remove", "--force", t.Path)
	return err
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
	base, err := git(ctx, t.Repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && base != t.Branch {
		s.Base = base
	}
	if s.Base != "" {
		if out, err := git(ctx, t.Path, "rev-list", "--count", s.Base+".."+t.Branch); err == nil {
			fmt.Sscanf(out, "%d", &s.Commits)
		}
	}
	if out, err := git(ctx, t.Path, "status", "--porcelain"); err == nil {
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
		return fmt.Sprintf("%s, plus uncommitted changes", plural(s.Commits, "commit"))
	default:
		return plural(s.Commits, "commit")
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// worktreeAt reports whether git already has a worktree checked out at path.
func worktreeAt(ctx context.Context, repo, path string) (bool, error) {
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}
	want := "worktree " + path
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true, nil
		}
	}
	return false, nil
}

func branchExists(ctx context.Context, repo, branch string) bool {
	_, err := git(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// sanitize makes a branch name safe as one path segment; a branch with
// slashes would otherwise nest directories under the worktree root.
func sanitize(branch string) string {
	return strings.NewReplacer("/", "-", string(filepath.Separator), "-").Replace(branch)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
