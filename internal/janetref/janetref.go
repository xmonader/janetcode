// Package janetref keeps janetcode's own records inside the repository
// they are about, as commits under refs/janetcode/: the probe memory
// (refs/janetcode/probes, see probes.go) and saved sessions
// (refs/janetcode/sessions/<id>, see sessions.go). A ref under
// refs/janetcode/ is never checked out, never on a branch and never moved
// by an ordinary `git push` or `git fetch`, so nothing here touches the
// user's history; Push and Fetch are the explicit step that moves the
// records to and from a remote, the way proof notes move (internal/proof).
//
// Each record is a commit whose tree holds a few named files, written
// with git's plumbing (hash-object, mktree, commit-tree) and published
// with a compare-and-swap update-ref, so two sessions writing the same
// ref at once cannot lose each other's write silently.
package janetref

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
)

// Prefix is the namespace every janetcode ref lives under.
const Prefix = "refs/janetcode/"

// DefaultRemote is the remote Push and Fetch use when none is named.
const DefaultRemote = "origin"

// ErrConflict is what Write returns when the ref moved between the read
// the caller built on and the write.
var ErrConflict = errors.New("the ref moved while it was being written")

// identity is the author of janetcode's record commits, so writing one
// works in a repository with no user.name configured and never puts the
// user's name on a commit they did not make.
var identity = []string{
	"GIT_AUTHOR_NAME=janetcode", "GIT_AUTHOR_EMAIL=janetcode@localhost",
	"GIT_COMMITTER_NAME=janetcode", "GIT_COMMITTER_EMAIL=janetcode@localhost",
}

// Tip is the commit ref points at, or "" when it does not exist.
func Tip(ctx context.Context, repo, ref string) (string, error) {
	out, err := gitx.Run(ctx, repo, nil, "rev-parse", "--verify", "-q", ref+"^{commit}")
	if err != nil {
		if gitx.ExitCode(err) == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Write records files as a new commit on ref whose parent is old (the tip
// the caller read; "" for a ref that must not exist yet), and moves ref to
// it only if ref still points at old. A file name may not contain '/'.
func Write(ctx context.Context, repo, ref, old, message string, files map[string][]byte) (string, error) {
	if !strings.HasPrefix(ref, Prefix) {
		return "", fmt.Errorf("%s is not under %s", ref, Prefix)
	}
	names := make([]string, 0, len(files))
	for n := range files {
		if n == "" || strings.ContainsAny(n, "/\x00\n\t") {
			return "", fmt.Errorf("bad record file name %q", n)
		}
		names = append(names, n)
	}
	sort.Strings(names)
	var tree strings.Builder
	for _, n := range names {
		blob, err := gitx.RunInput(ctx, repo, files[n], "hash-object", "-w", "--stdin")
		if err != nil {
			return "", gitx.Wrapf(err, "git hash-object")
		}
		fmt.Fprintf(&tree, "100644 blob %s\t%s\n", strings.TrimSpace(string(blob)), n)
	}
	treeID, err := gitx.RunInput(ctx, repo, []byte(tree.String()), "mktree")
	if err != nil {
		return "", gitx.Wrapf(err, "git mktree")
	}
	args := []string{"commit-tree", strings.TrimSpace(string(treeID)), "-m", message}
	if old != "" {
		args = append(args, "-p", old)
	}
	commit, err := gitx.RunInputEnv(ctx, repo, identity, nil, args...)
	if err != nil {
		return "", gitx.Wrapf(err, "git commit-tree")
	}
	sha := strings.TrimSpace(string(commit))
	// update-ref's third argument is the value ref must have now; the
	// all-zero name says it must not exist.
	expect := old
	if expect == "" {
		expect = strings.Repeat("0", len(sha))
	}
	if _, err := gitx.Run(ctx, repo, nil, "update-ref", "-m", "janetcode: "+message, ref, sha, expect); err != nil {
		if now, terr := Tip(ctx, repo, ref); terr == nil && now != old {
			return "", ErrConflict
		}
		return "", gitx.Wrapf(err, "git update-ref")
	}
	return sha, nil
}

// Read is one file of the record ref points at; ok is false when the ref
// or the file does not exist.
func Read(ctx context.Context, repo, ref, name string) (data []byte, ok bool, err error) {
	tip, err := Tip(ctx, repo, ref)
	if err != nil || tip == "" {
		return nil, false, err
	}
	return ReadAt(ctx, repo, tip, name)
}

// ReadAt is Read at a known commit.
func ReadAt(ctx context.Context, repo, commit, name string) (data []byte, ok bool, err error) {
	if _, err := gitx.Run(ctx, repo, nil, "cat-file", "-e", commit+":"+name); err != nil {
		return nil, false, nil
	}
	data, err = gitx.RunBytes(ctx, repo, nil, "cat-file", "blob", commit+":"+name)
	if err != nil {
		return nil, false, gitx.Wrapf(err, "git cat-file")
	}
	return data, true, nil
}

// Files lists the file names and sizes of the record at commit.
func Files(ctx context.Context, repo, commit string) (map[string]int64, error) {
	out, err := gitx.Run(ctx, repo, nil, "ls-tree", "-l", commit)
	if err != nil {
		return nil, gitx.Wrapf(err, "git ls-tree")
	}
	files := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		meta, name, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) < 4 {
			continue
		}
		var size int64
		fmt.Sscan(f[3], &size)
		files[name] = size
	}
	return files, nil
}

// Refs lists the refs under prefix (e.g. Prefix+"sessions/"), sorted.
func Refs(ctx context.Context, repo, prefix string) ([]string, error) {
	out, err := gitx.Run(ctx, repo, nil, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return nil, gitx.Wrapf(err, "git for-each-ref")
	}
	var refs []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			refs = append(refs, l)
		}
	}
	sort.Strings(refs)
	return refs, nil
}

// Push pushes every ref matching the patterns ("refs/janetcode/probes",
// "refs/janetcode/sessions/*") that exists here to remote. A pattern
// with nothing behind it is left out rather than failing the push.
func Push(ctx context.Context, repo, remote string, patterns ...string) ([]string, error) {
	if strings.TrimSpace(remote) == "" {
		remote = DefaultRemote
	}
	if err := notAnOption("remote", remote); err != nil {
		return nil, err
	}
	var refs []string
	for _, p := range patterns {
		got, err := Refs(ctx, repo, strings.TrimSuffix(p, "*"))
		if err != nil {
			return nil, err
		}
		for _, r := range got {
			if strings.HasSuffix(p, "*") || r == p {
				refs = append(refs, r)
			}
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	// Records are append-only histories written by one machine each, but
	// a probe ledger is shared: a rejected push is the caller's to fetch
	// and retry, never forced over someone else's.
	if _, err := gitx.Run(ctx, repo, nil, append([]string{"push", remote}, refs...)...); err != nil {
		return nil, err
	}
	return refs, nil
}

// Fetch fetches the refs matching pattern from remote into the same names
// here. Sessions are fetched forced (each is one machine's record), the
// probe ledger is not: it is merged by the caller (see FetchProbes).
func Fetch(ctx context.Context, repo, remote, src, dst string) error {
	if strings.TrimSpace(remote) == "" {
		remote = DefaultRemote
	}
	if err := notAnOption("remote", remote); err != nil {
		return err
	}
	if !strings.HasPrefix(dst, Prefix) {
		return fmt.Errorf("%s is not under %s", dst, Prefix)
	}
	_, err := gitx.Run(ctx, repo, nil, "fetch", "--no-tags", remote, src+":"+dst)
	return err
}

// notAnOption refuses a user-typed name git would read as an option.
func notAnOption(what, name string) error {
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("%s %q starts with '-'; git would read it as an option", what, name)
	}
	return nil
}
