package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
)

// worktree is a throwaway checkout of the project for an editing
// subagent: its changes come back as a patch and never touch the real
// tree until the main agent applies them.
type worktree struct {
	Root string // the real repository
	Dir  string // the checkout
	base string // commit the checkout started from
	snap *snapshot.Snap
	repo *snapshot.Repo
}

// newWorktree checks the current state of root out into a temp directory.
// Uncommitted and untracked changes are included through a snapshot.
func newWorktree(ctx context.Context, root string) (*worktree, error) {
	repo, err := snapshot.Open(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("editing subagents need a git repository: %w", err)
	}
	w := &worktree{Root: repo.Root, repo: repo}
	snap, err := repo.Take(ctx, "subagent base")
	if err != nil {
		return nil, fmt.Errorf("snapshot for the worktree: %w", err)
	}
	w.snap, w.base = &snap, snap.Commit
	// Until the checkout exists the caller has nothing to remove, so a
	// failure here drops the base snapshot's ref (and whatever a failed
	// add left in the directory) itself rather than leaking them.
	dir, err := os.MkdirTemp("", "janetcode-wt-")
	if err != nil {
		repo.Drop(context.WithoutCancel(ctx), snap)
		return nil, err
	}
	os.Remove(dir) // git wants to create it
	if out, err := gitRun(ctx, repo.Root, "worktree", "add", "--detach", "-q", dir, w.base); err != nil {
		os.RemoveAll(dir)
		repo.Drop(context.WithoutCancel(ctx), snap)
		return nil, fmt.Errorf("git worktree add: %v: %s", err, out)
	}
	w.Dir = dir
	return w, nil
}

// diff stages everything in the checkout and returns the patch and a
// --stat summary against the base.
func (w *worktree) diff(ctx context.Context) (patch, stat string, err error) {
	if out, err := gitRun(ctx, w.Dir, "add", "-A", "--", "."); err != nil {
		return "", "", fmt.Errorf("git add: %v: %s", err, out)
	}
	cmd := exec.CommandContext(ctx, "git", "diff", "--cached", "--binary", w.base)
	cmd.Dir = w.Dir
	raw, err := cmd.Output() // not trimmed: a patch needs its final newline
	if err != nil {
		return "", "", fmt.Errorf("git diff: %v", err)
	}
	patch = string(raw)
	stat, err = gitRun(ctx, w.Dir, "diff", "--cached", "--stat", w.base)
	if err != nil {
		return "", "", fmt.Errorf("git diff --stat: %v: %s", err, stat)
	}
	return patch, strings.TrimSpace(stat), nil
}

// remove deletes the checkout and the base snapshot.
func (w *worktree) remove(ctx context.Context) {
	if w.Dir != "" {
		_, _ = gitRun(ctx, w.Root, "worktree", "remove", "--force", w.Dir)
		os.RemoveAll(w.Dir)
		_, _ = gitRun(ctx, w.Root, "worktree", "prune")
	}
	if w.snap != nil {
		w.repo.Drop(ctx, *w.snap)
	}
}

// tools is the full editing tool set rooted at the checkout.
func (w *worktree) tools(cache *tool.IOCache) []tool.Tool {
	seen := &tool.Seen{}
	return []tool.Tool{
		tool.ReadFile{Root: w.Dir, Seen: seen, Cache: cache}, tool.WriteFile{Root: w.Dir, Seen: seen, Cache: cache}, tool.EditFile{Root: w.Dir, Seen: seen, Cache: cache},
		tool.ListDir{Root: w.Dir}, tool.Glob{Root: w.Dir}, tool.Grep{Root: w.Dir},
		tool.Bash{Root: w.Dir, Cache: &tool.ShellCache{}},
	}
}

// savePatch writes the patch where the main agent can git apply it.
func savePatch(patch, label string) (string, error) {
	dir := filepath.Join(os.TempDir(), "janetcode-patches")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, sanitizeLabel(label)+"-*.patch")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(patch); err != nil {
		f.Close()
		return "", err
	}
	return f.Name(), f.Close()
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	if b.Len() == 0 {
		return "task"
	}
	return strings.Trim(b.String(), "-")
}

func gitRun(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=janetcode", "GIT_AUTHOR_EMAIL=janetcode@localhost", "GIT_COMMITTER_NAME=janetcode", "GIT_COMMITTER_EMAIL=janetcode@localhost")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
