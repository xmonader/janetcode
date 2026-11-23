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
	// env is set for a shadow repository (see OpenShadow): GIT_DIR and
	// GIT_WORK_TREE, so every git call works on the private store over a
	// directory that is not a repository itself.
	env []string
	// raw keeps git from converting file content on its way in or out
	// (gitx.ByteExactEnv): an undo that turned LF into CRLF under Git for
	// Windows' autocrlf would itself be a change to every restored file.
	raw []string
}

// Shadow reports whether this is a private store over a directory that
// is not a git repository (OpenShadow): its snapshots serve /undo, but it
// has no history of the user's, so nothing may treat it as one — proof
// notes, a session's start commit, an eval replay.
func (r *Repo) Shadow() bool { return r != nil && r.env != nil }

// Open finds the repository containing dir, or ErrNotRepo.
func Open(ctx context.Context, dir string) (*Repo, error) {
	top, err := gitx.Run(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNotRepo
	}
	gd, err := gitx.Run(ctx, dir, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrNotRepo
	}
	r := &Repo{Root: top, gitDir: gd}
	r.raw = r.byteExact(ctx)
	return r, nil
}

// byteExact is gitx.ByteExactEnv for this repository, its empty tree
// worked out once here rather than on every git call.
func (r *Repo) byteExact(ctx context.Context) []string {
	empty, err := gitx.Run(ctx, r.Root, r.env, "hash-object", "-t", "tree", os.DevNull)
	if err != nil {
		empty = ""
	}
	return gitx.ByteExactEnv(strings.TrimSpace(empty))
}

// Snap identifies one snapshot.
type Snap struct {
	Commit string `json:"commit"`
	Head   string `json:"head"` // HEAD when it was taken; "" on an unborn branch
	// Root is the work tree it was taken of. Two worktrees of one
	// repository share their objects and can stand at the same HEAD, so
	// without it a session that moved (/cd) from one to the other
	// restored the first one's tree over the second. "" in a snapshot
	// recorded before this field existed.
	Root string `json:"root,omitempty"`
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
	if !r.seedFromIndex(ctx, idx) {
		if head != "" {
			if _, err := r.run(ctx, idx, "read-tree", head); err != nil {
				return Snap{}, err
			}
		} else if _, err := r.run(ctx, idx, "read-tree", "--empty"); err != nil {
			return Snap{}, err
		}
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
	return Snap{Commit: commit, Head: head, Root: r.Root}, nil
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
	if s.Root != "" && s.Root != r.Root {
		return nil, fmt.Errorf("%w: the snapshot is of %s, not %s", ErrHeadMoved, s.Root, r.Root)
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
	out, err := gitx.RunInputEnv(ctx, r.Root, r.env, created, "check-ignore", "--no-index", "--stdin", "-z")
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

// Uncovered returns the paths among paths (absolute) that restoring the
// snapshot does not account for: outside the work tree, inside .git,
// below a symlink, or ignored and absent from the snapshot. Restore
// neither puts such a path back nor removes it, so whoever holds another
// copy of it — the file tools' own — has to.
func (r *Repo) Uncovered(ctx context.Context, s Snap, paths []string) ([]string, error) {
	idx, cleanup, err := r.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	// The snapshot's own tree as the index: a path it holds is tracked,
	// which check-ignore never reports, however the rules read now.
	if _, err := r.run(ctx, idx, "read-tree", s.Commit); err != nil {
		return nil, err
	}
	var (
		out  []string
		rels []string
		abs  = map[string][]string{}
	)
	for _, p := range paths {
		rel, ok := r.rel(p)
		if !ok {
			out = append(out, p)
			continue
		}
		if abs[rel] == nil {
			rels = append(rels, rel)
		}
		abs[rel] = append(abs[rel], p)
	}
	ignored := func(rels ...string) ([]string, error) {
		var in []byte
		for _, rel := range rels {
			in = append(append(in, rel...), 0)
		}
		o, err := gitx.RunInputEnv(ctx, r.Root, append([]string{"GIT_INDEX_FILE=" + idx}, r.env...), in, "check-ignore", "--stdin", "-z")
		if gitx.ExitCode(err) == 1 {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return strings.Split(strings.TrimSuffix(string(o), "\x00"), "\x00"), nil
	}
	found, err := ignored(rels...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		// One path git refuses outright (below a symlink, in a nested
		// repository) fails the whole call. Asked one at a time, such a
		// path is one `git add -A` did not record either.
		found = nil
		for _, rel := range rels {
			f, err := ignored(rel)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil {
				f = []string{rel}
			}
			found = append(found, f...)
		}
	}
	for _, rel := range found {
		out = append(out, abs[rel]...)
	}
	return out, nil
}

// rel is p relative to the work tree in git's spelling, or false when p
// is not somewhere a snapshot records: outside the tree, or in .git. The
// directory part is resolved through symlinks, since git reports the top
// level with them resolved (/tmp is /private/tmp on macOS).
func (r *Repo) rel(p string) (string, bool) {
	dir, base := filepath.Dir(p), filepath.Base(p)
	for {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			p = filepath.Join(real, base)
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		base = filepath.Join(filepath.Base(dir), base)
		dir = parent
	}
	root := r.Root
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	rel, err := filepath.Rel(root, p)
	sep := string(filepath.Separator)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+sep) || rel == ".git" || strings.HasPrefix(rel, ".git"+sep) {
		return "", false
	}
	return filepath.ToSlash(rel), true
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
	return head != s.Head || s.Root != "" && s.Root != r.Root, nil
}

// Export writes the tree a snapshot recorded into dir, which must exist
// and should be empty, without touching the work tree, HEAD or the real
// index. Files that were ignored when the snapshot was taken are not in
// it, so neither are they in dir. The probe ledger runs a check against
// the code as it was before a turn's first change this way.
func (r *Repo) Export(ctx context.Context, s Snap, dir string) error {
	if s.Commit == "" {
		return errors.New("snapshot has no commit")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	idx, cleanup, err := r.tempIndex()
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := r.run(ctx, idx, "read-tree", s.Commit); err != nil {
		return err
	}
	_, err = r.run(ctx, idx, "checkout-index", "-a", "-f", "--prefix="+abs+string(filepath.Separator))
	return err
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
	h, err := gitx.Run(ctx, r.Root, r.env, "rev-parse", "--verify", "-q", "HEAD")
	if gitx.ExitCode(err) == 1 {
		return "", nil
	}
	return h, err
}

func (r *Repo) tempIndex() (string, func(), error) {
	p := filepath.Join(r.gitDir, "janetcode-index-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	return p, func() { os.Remove(p) }, nil
}

// seedFromIndex starts the temporary index at dst from a copy of the
// repository's own index and reports whether it did. `add -A` makes an
// index match the work tree whatever it started as, so the snapshot's
// tree is the same either way; what differs is the cost. A fresh
// `read-tree HEAD` index has no stat cache, so `add -A` re-reads and
// re-hashes every file in the tree (10 s on kubernetes' 31k files, once
// per session start and once per turn that mutates), where the copied
// index lets git skip every file whose stat data is unchanged. It
// declines, and the caller falls back to read-tree, for a shadow
// repository (no index of its own), a missing or unreadable index, and a
// sparse checkout, whose skip-worktree entries `add -A` would keep as
// they are rather than reconcile.
func (r *Repo) seedFromIndex(ctx context.Context, dst string) bool {
	if r.env != nil {
		return false
	}
	if out, err := r.run(ctx, "", "config", "--get", "core.sparseCheckout"); err == nil && strings.TrimSpace(out) != "false" && strings.TrimSpace(out) != "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(r.gitDir, "index"))
	if err != nil || len(data) == 0 {
		return false
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		os.Remove(dst)
		return false
	}
	// Racy git: git trusts an entry whose size and mtime match the file
	// only when the file is older than the index file; an entry at or
	// after the index's mtime is re-hashed. The copy is newer than the
	// original, so without this its entries all look safely older and a
	// same-size edit made in the same timestamp granule as the original
	// index write is skipped, snapshotting stale content. Giving the copy
	// the original's mtime keeps git's racy check exact at no cost.
	if fi, err := os.Stat(filepath.Join(r.gitDir, "index")); err != nil || os.Chtimes(dst, fi.ModTime(), fi.ModTime()) != nil {
		os.Remove(dst)
		return false
	}
	// An assume-unchanged (lowercase tag) or skip-worktree (S) entry
	// would carry over, and add -A never looks at its file: an edit to it
	// would be missing from the snapshot. Decline when any entry has one.
	out, err := r.run(ctx, dst, "ls-files", "-v")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if line != "" && (line[0] == 'S' || (line[0] >= 'a' && line[0] <= 'z')) {
			return false
		}
	}
	return true
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
	env = append(env, r.env...)
	env = append(env, r.raw...)
	return gitx.Run(ctx, r.Root, env, args...)
}
