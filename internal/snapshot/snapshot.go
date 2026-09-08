// Package snapshot records the whole working tree of a git repository as a
// hidden commit, without touching HEAD, the index or the user's branches,
// and restores the tree from such a commit. It lets /undo revert changes
// made by shell commands, not only by the file tools.
package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RefPrefix is where snapshot commits are anchored so gc keeps them.
const RefPrefix = "refs/janetcode/cp/"

// ErrHeadMoved means the branch tip changed since the snapshot (a commit,
// checkout or reset happened), so restoring it would mix two states.
var ErrHeadMoved = errors.New("HEAD has moved since the snapshot was taken")

// ErrNotRepo means the directory is not inside a git work tree.
var ErrNotRepo = errors.New("not a git repository")

// Repo is a git work tree that snapshots are taken of.
type Repo struct {
	Root   string // top level of the work tree
	gitDir string
}

// Open finds the repository containing dir, or ErrNotRepo.
func Open(dir string) (*Repo, error) {
	top, err := git(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, ErrNotRepo
	}
	gd, err := git(dir, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, ErrNotRepo
	}
	return &Repo{Root: top, gitDir: gd}, nil
}

// Snap identifies one snapshot.
type Snap struct {
	Commit string `json:"commit"`
	Head   string `json:"head"` // HEAD when it was taken; "" on an unborn branch
}

// Take records the current work tree (tracked, modified and untracked
// files; ignored files excluded) and returns the snapshot.
func (r *Repo) Take(ctx context.Context, label string) (Snap, error) {
	idx, cleanup, err := r.tempIndex()
	if err != nil {
		return Snap{}, err
	}
	defer cleanup()
	head := r.head()
	if head != "" {
		if _, err := r.run(ctx, idx, "read-tree", head); err != nil {
			return Snap{}, err
		}
	} else if _, err := r.run(ctx, idx, "read-tree", "--empty"); err != nil {
		return Snap{}, err
	}
	if _, err := r.run(ctx, idx, "add", "-A", "--", "."); err != nil {
		return Snap{}, err
	}
	tree, err := r.run(ctx, idx, "write-tree")
	if err != nil {
		return Snap{}, err
	}
	args := []string{"commit-tree", tree, "-m", "janetcode snapshot: " + label}
	if head != "" {
		args = append(args, "-p", head)
	}
	commit, err := r.run(ctx, idx, args...)
	if err != nil {
		return Snap{}, err
	}
	if _, err := r.run(ctx, "", "update-ref", RefPrefix+commit, commit); err != nil {
		return Snap{}, err
	}
	return Snap{Commit: commit, Head: head}, nil
}

// Restore puts the work tree back to the snapshot: files changed or
// deleted since are rewritten and files created since are removed. The
// real index is left alone. It returns a line per file it touched.
func (r *Repo) Restore(ctx context.Context, s Snap) ([]string, error) {
	if r.head() != s.Head {
		return nil, ErrHeadMoved
	}
	if _, err := r.run(ctx, "", "cat-file", "-e", s.Commit+"^{commit}"); err != nil {
		return nil, fmt.Errorf("snapshot %s is gone: %w", short(s.Commit), err)
	}
	idx, cleanup, err := r.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	// Stage the current tree in the temporary index so diff-index can
	// compare it to the snapshot, untracked files included.
	if _, err := r.run(ctx, idx, "read-tree", s.Commit); err != nil {
		return nil, err
	}
	if _, err := r.run(ctx, idx, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, idx, "diff-index", "--cached", "--name-status", "-z", s.Commit)
	if err != nil {
		return nil, err
	}
	var (
		done    []string
		errs    []error
		restore []string
	)
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		abs := filepath.Join(r.Root, path)
		switch status[0] {
		case 'A': // created since the snapshot
			if err := os.Remove(abs); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
				continue
			}
			done = append(done, "removed "+abs)
		default: // modified or deleted since the snapshot
			restore = append(restore, path)
			done = append(done, "restored "+abs)
		}
	}
	if len(restore) > 0 {
		args := append([]string{"checkout", s.Commit, "--"}, restore...)
		if _, err := r.run(ctx, idx, args...); err != nil {
			errs = append(errs, err)
		}
	}
	return done, errors.Join(errs...)
}

// Drop deletes the ref anchoring a snapshot so gc can reclaim it.
func (r *Repo) Drop(ctx context.Context, s Snap) {
	if s.Commit != "" {
		r.run(ctx, "", "update-ref", "-d", RefPrefix+s.Commit)
	}
}

// Changed reports whether the work tree differs from the snapshot.
func (r *Repo) Changed(ctx context.Context, s Snap) (bool, error) {
	idx, cleanup, err := r.tempIndex()
	if err != nil {
		return false, err
	}
	defer cleanup()
	if _, err := r.run(ctx, idx, "read-tree", s.Commit); err != nil {
		return false, err
	}
	if _, err := r.run(ctx, idx, "add", "-A", "--", "."); err != nil {
		return false, err
	}
	out, err := r.run(ctx, idx, "diff-index", "--cached", "--name-only", s.Commit)
	return out != "", err
}

func (r *Repo) head() string {
	h, err := git(r.Root, nil, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		return ""
	}
	return h
}

func (r *Repo) tempIndex() (string, func(), error) {
	p := filepath.Join(r.gitDir, "janetcode-index-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	return p, func() { os.Remove(p) }, nil
}

// run executes git in the work tree; when index is set the command works
// on that index file instead of the real one.
func (r *Repo) run(ctx context.Context, index string, args ...string) (string, error) {
	env := []string{
		"GIT_AUTHOR_NAME=janetcode", "GIT_AUTHOR_EMAIL=janetcode@localhost",
		"GIT_COMMITTER_NAME=janetcode", "GIT_COMMITTER_EMAIL=janetcode@localhost",
	}
	if index != "" {
		env = append(env, "GIT_INDEX_FILE="+index)
	}
	return gitCtx(ctx, r.Root, env, args...)
}

func git(dir string, env []string, args ...string) (string, error) {
	return gitCtx(context.Background(), dir, env, args...)
}

func gitCtx(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
