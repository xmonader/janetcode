// Package snapshot records the whole working tree of a git repository as a
// hidden commit, without touching HEAD, the index or the user's branches,
// and restores the tree from such a commit. It lets /undo revert changes
// made by shell commands, not only by the file tools.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
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
func Open(ctx context.Context, dir string) (*Repo, error) {
	top, err := gitx.Run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, ErrNotRepo
	}
	gd, err := gitx.Run(ctx, dir, nil, "rev-parse", "--absolute-git-dir")
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
	head, err := r.head(ctx)
	if err != nil {
		return Snap{}, err
	}
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
	return r.restore(ctx, s, nil)
}

// RestoreSkip is Restore with a set of paths to leave alone — undo
// passes the files that changed after the snapshot's turn ended, whose
// current state is not the turn's work and must survive the restore.
func (r *Repo) RestoreSkip(ctx context.Context, s Snap, skip map[string]bool) ([]string, error) {
	return r.restore(ctx, s, skip)
}

func (r *Repo) restore(ctx context.Context, s Snap, skip map[string]bool) ([]string, error) {
	head, err := r.head(ctx)
	if err != nil {
		return nil, err
	}
	if head != s.Head {
		return nil, ErrHeadMoved
	}
	if _, err := r.run(ctx, "", "cat-file", "-e", s.Commit+"^{commit}"); err != nil {
		return nil, fmt.Errorf("snapshot %s is gone: %w", gitx.Short(s.Commit), err)
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
	out, err := r.run(ctx, idx, "diff-index", "--cached", "--no-renames", "--name-status", "-z", s.Commit)
	if err != nil {
		return nil, err
	}
	var (
		done []string
		errs []error
		// The ignore rules go back first, on their own: which of the
		// other paths count as created since the snapshot depends on
		// them (see keepIgnored).
		rules, rest [][2]string
	)
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		if status == "" || path == "" {
			continue
		}
		if skip != nil && skip[filepath.Join(r.Root, path)] {
			continue
		}
		if filepath.Base(path) == ".gitignore" {
			rules = append(rules, [2]string{status, path})
		} else {
			rest = append(rest, [2]string{status, path})
		}
	}
	d, err := r.apply(ctx, idx, s, rules)
	done, errs = append(done, d...), append(errs, err)
	rest, err = r.keepIgnored(ctx, rest)
	errs = append(errs, err)
	d, err = r.apply(ctx, idx, s, rest)
	done, errs = append(done, d...), append(errs, err)
	return done, errors.Join(errs...)
}

// keepIgnored drops from items every created-since path the ignore rules
// — as restored — exclude. The snapshot never records an ignored file, so
// one it does not hold may simply have been ignored when it was taken:
// a turn that deleted a line from .gitignore made the user's .env, or a
// whole node_modules, read as created by the turn, and the restore
// deleted them. Such a path is left alone. When git cannot say, nothing
// is removed at all.
func (r *Repo) keepIgnored(ctx context.Context, items [][2]string) ([][2]string, error) {
	var created []byte
	for _, it := range items {
		if it[0][0] == 'A' {
			created = append(append(created, it[1]...), 0)
		}
	}
	if len(created) == 0 {
		return items, nil
	}
	// --no-index: a created path is by definition not in the snapshot,
	// and the real index is not what the snapshot was built from.
	out, err := gitx.RunInput(ctx, r.Root, created, "check-ignore", "--no-index", "--stdin", "-z")
	ignored := map[string]bool{}
	switch {
	case err == nil:
		for _, p := range strings.Split(string(out), "\x00") {
			ignored[p] = true
		}
	case gitx.ExitCode(err) == 1: // none of them is ignored
	default:
		ignored = nil
	}
	var kept [][2]string
	for _, it := range items {
		if it[0][0] == 'A' && (ignored == nil || ignored[it[1]]) {
			continue
		}
		kept = append(kept, it)
	}
	if ignored == nil {
		return kept, fmt.Errorf("files created since the snapshot were left in place, since git could not say which of them it ignores: %w", err)
	}
	return kept, nil
}

// apply removes the items created since the snapshot and checks the rest
// out of it, returning a line per path.
func (r *Repo) apply(ctx context.Context, idx string, s Snap, items [][2]string) ([]string, error) {
	var (
		done    []string
		errs    []error
		restore []string
	)
	for _, it := range items {
		status, path := it[0], it[1]
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

// Moved reports whether HEAD is no longer where the snapshot was taken,
// which is exactly what makes Restore refuse with ErrHeadMoved. A caller
// that wants to say so before touching anything (see /undo turn) asks
// here rather than finding out halfway through a restore.
func (r *Repo) Moved(ctx context.Context, s Snap) (bool, error) {
	head, err := r.head(ctx)
	if err != nil {
		return false, err
	}
	return head != s.Head, nil
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
	out, err := r.run(ctx, idx, "diff-index", "--cached", "--no-renames", "--name-only", s.Commit)
	return out != "", err
}

// Diff lists how the work tree differs from a snapshot as "M path",
// "A path" (exists now, not in the snapshot) or "D path" (gone now).
func (r *Repo) Diff(ctx context.Context, s Snap) ([]string, error) {
	idx, cleanup, err := r.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if _, err := r.run(ctx, idx, "read-tree", s.Commit); err != nil {
		return nil, err
	}
	if _, err := r.run(ctx, idx, "add", "-A", "--", "."); err != nil {
		return nil, err
	}
	out, err := r.run(ctx, idx, "diff-index", "--cached", "--no-renames", "--name-status", "-z", s.Commit)
	if err != nil {
		return nil, err
	}
	var lines []string
	parts := strings.Split(out, "\x00")
	for i := 0; i+1 < len(parts); i += 2 {
		status, path := parts[i], parts[i+1]
		if status == "" || path == "" {
			continue
		}
		lines = append(lines, status[:1]+" "+filepath.Join(r.Root, path))
	}
	return lines, nil
}

// head is the commit HEAD names, or "" on an unborn branch (rev-parse -q
// exits 1 for a missing revision). Any other failure is an error, so a
// cancelled ctx is not mistaken for an unborn branch.
func (r *Repo) head(ctx context.Context) (string, error) {
	h, err := gitx.Run(ctx, r.Root, nil, "rev-parse", "--verify", "-q", "HEAD")
	if gitx.ExitCode(err) == 1 {
		return "", nil
	}
	return h, err
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
	return gitx.Run(ctx, r.Root, env, args...)
}
