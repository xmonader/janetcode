package eval

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
)

// The invariant: janetcode leaves nothing in the repository it worked in.
// Its per-project state lives under .janetcode/ (plan.md, uploads,
// recipes, evals, skills, ... — anything it writes there); in a git
// repository the only refs it makes are its own records under
// refs/janetcode/ (checkpoint anchors cp/, probes, saved sessions) and
// proof notes under refs/notes/janetcode. Those two namespaces are
// janetcode's by design (internal/janetref, internal/snapshot,
// internal/proof) and are never counted; a new branch, tag, stash or any
// other ref is, as is any .janetcode/ path, in the work tree, the index
// or a commit, that the task's starting tree did not already hold (a
// fixture can ship .janetcode/verify.json).

var allowedRefPrefixes = []string{"refs/janetcode/", "refs/notes/janetcode"}

// stateBefore is the work directory's state at the start of a run.
type stateBefore struct {
	paths map[string]bool // .janetcode/ paths present, slash-separated
	refs  map[string]bool // refs present, git repositories only
	git   bool
}

func isGitDir(ctx context.Context, dir string) bool {
	_, err := gitx.Run(ctx, dir, nil, "rev-parse", "--git-dir")
	return err == nil
}

func statePaths(dir string) map[string]bool {
	out := map[string]bool{}
	_ = filepath.WalkDir(filepath.Join(dir, ".janetcode"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if rel, rerr := filepath.Rel(dir, p); rerr == nil {
			out[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	return out
}

func listRefs(ctx context.Context, dir string) map[string]bool {
	out := map[string]bool{}
	s, err := gitx.Run(ctx, dir, nil, "for-each-ref", "--format=%(refname)")
	if err != nil {
		return out
	}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out[l] = true
		}
	}
	return out
}

// snapshotState records what dir holds before the agent runs.
func snapshotState(ctx context.Context, dir string) stateBefore {
	b := stateBefore{paths: statePaths(dir)}
	if b.git = isGitDir(ctx, dir); b.git {
		b.refs = listRefs(ctx, dir)
	}
	return b
}

// pollution lists what janetcode left in dir that before did not hold; it
// is empty when the invariant holds. allowRefs lets a task that asks for a
// commit (which can create a branch) skip the ref check.
func pollution(ctx context.Context, dir string, before stateBefore, allowRefs bool) []string {
	var out []string
	for p := range statePaths(dir) {
		if !before.paths[p] {
			out = append(out, "left "+p+" in the repo")
		}
	}
	if before.git {
		seen := map[string]bool{}
		for _, args := range [][]string{
			{"ls-files", "--", ".janetcode"},
			{"log", "--all", "--name-only", "--format=", "--", ".janetcode"},
		} {
			s, err := gitx.Run(ctx, dir, nil, args...)
			if err != nil {
				continue
			}
			for _, l := range strings.Split(s, "\n") {
				if l = strings.TrimSpace(l); l != "" && !before.paths[l] && !seen[l] {
					seen[l] = true
					out = append(out, "committed or staged "+l)
				}
			}
		}
		if !allowRefs {
			for ref := range listRefs(ctx, dir) {
				if !before.refs[ref] && !allowedRef(ref) {
					out = append(out, "left ref "+ref)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func allowedRef(ref string) bool {
	for _, p := range allowedRefPrefixes {
		if strings.HasPrefix(ref, p) {
			return true
		}
	}
	return false
}
