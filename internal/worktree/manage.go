package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/gitx"
)

// Info is one managed worktree: a checkout janetcode made under the
// repository's git directory (Open).
type Info struct {
	Branch  string
	Path    string
	Dirty   bool
	Commits int // commits on the branch that the repository's own HEAD lacks
	Missing bool
}

// root is the directory managed worktrees live in.
func root(ctx context.Context, repo string) (string, error) {
	common, err := gitx.Run(ctx, repo, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("worktree: %s is not a git repository", repo)
	}
	return filepath.Join(common, "janetcode-worktrees"), nil
}

// List returns the managed worktrees of the repository, sorted by branch.
func List(ctx context.Context, repo string) ([]Info, error) {
	dir, err := root(ctx, repo)
	if err != nil {
		return nil, err
	}
	out, err := gitx.Run(ctx, repo, nil, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	base := resolve(dir)
	var infos []Info
	var cur *Info
	flush := func() {
		if cur != nil {
			infos = append(infos, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			p := strings.TrimPrefix(line, "worktree ")
			if strings.HasPrefix(resolve(p), base+string(filepath.Separator)) {
				cur = &Info{Path: p}
			}
		case cur != nil && strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		case cur != nil && line == "prunable" || cur != nil && strings.HasPrefix(line, "prunable "):
			cur.Missing = true
		}
	}
	flush()
	head, _ := gitx.Run(ctx, repo, nil, "rev-parse", "--abbrev-ref", "HEAD")
	for i := range infos {
		in := &infos[i]
		if _, err := os.Stat(in.Path); err != nil {
			in.Missing = true
			continue
		}
		if st, err := gitx.Run(ctx, in.Path, nil, "status", "--porcelain"); err == nil {
			in.Dirty = strings.TrimSpace(st) != ""
		}
		if head != "" && head != "HEAD" && in.Branch != "" {
			if n, err := gitx.Run(ctx, repo, nil, "rev-list", "--count", head+".."+in.Branch); err == nil {
				fmt.Sscanf(n, "%d", &in.Commits)
			}
		}
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Branch < infos[j].Branch })
	return infos, nil
}

// Remove deletes the managed worktree for name (a branch or its directory
// name). A checkout with uncommitted changes is refused unless force is
// set; the branch itself is always kept, so committed work is never lost.
// keepPath, when set, is the session's own working directory, which is
// never removed from under it.
func Remove(ctx context.Context, repo, name, keepPath string, force bool) (Info, error) {
	infos, err := List(ctx, repo)
	if err != nil {
		return Info{}, err
	}
	name = strings.TrimSpace(name)
	var hit *Info
	for i := range infos {
		if infos[i].Branch == name || filepath.Base(infos[i].Path) == sanitize(name) {
			hit = &infos[i]
			break
		}
	}
	if hit == nil {
		return Info{}, fmt.Errorf("worktree: no managed worktree %q · /worktree list shows them", name)
	}
	if keepPath != "" && within(resolve(keepPath), resolve(hit.Path)) {
		return *hit, fmt.Errorf("worktree: %s is where this session is working; leave it first", hit.Branch)
	}
	if hit.Dirty && !force {
		return *hit, fmt.Errorf("worktree: %s has uncommitted changes that removing would lose · rerun with --force to discard them", hit.Branch)
	}
	args := []string{"worktree", "remove"}
	if force || hit.Missing {
		args = append(args, "--force")
	}
	args = append(args, hit.Path)
	if _, err := gitx.Run(ctx, repo, nil, args...); err != nil {
		return *hit, gitx.Wrapf(err, "worktree: git %s", strings.Join(args, " "))
	}
	_, _ = gitx.Run(ctx, repo, nil, "worktree", "prune")
	return *hit, nil
}

// Result is what /worktree decided: text to show, and a directory the
// session should move into (empty when it stays put).
type Result struct {
	Text  string
	Enter string
	Err   error
}

// Command runs /worktree arg for a session working in cwd: new <name>,
// list, remove <name> [--force].
func Command(ctx context.Context, cwd, arg string) Result {
	f := strings.Fields(arg)
	if len(f) == 0 {
		f = []string{"list"}
	}
	switch f[0] {
	case "new", "create", "add":
		if len(f) < 2 {
			return Result{Err: fmt.Errorf("usage: /worktree new <name>")}
		}
		wt, err := Open(ctx, cwd, f[1])
		if err != nil {
			return Result{Err: err}
		}
		verb := "reusing"
		if wt.Created {
			verb = "created"
		}
		return Result{Text: fmt.Sprintf("worktree: %s %s on branch %s · session moved there", verb, wt.Path, wt.Branch), Enter: wt.Path}
	case "list", "ls":
		infos, err := List(ctx, cwd)
		if err != nil {
			return Result{Err: err}
		}
		return Result{Text: Format(infos, cwd)}
	case "remove", "rm", "delete":
		force := false
		var name string
		for _, a := range f[1:] {
			if a == "--force" || a == "-f" {
				force = true
			} else {
				name = a
			}
		}
		if name == "" {
			return Result{Err: fmt.Errorf("usage: /worktree remove <name> [--force]")}
		}
		in, err := Remove(ctx, cwd, name, cwd, force)
		if err != nil {
			return Result{Err: err}
		}
		return Result{Text: fmt.Sprintf("worktree: removed %s · branch %s is kept (git branch -d %s when merged)", in.Path, in.Branch, in.Branch)}
	}
	return Result{Err: fmt.Errorf("usage: /worktree new <name> | list | remove <name> [--force]")}
}

// Format lists worktrees one per line, marking the one cwd is in.
func Format(infos []Info, cwd string) string {
	if len(infos) == 0 {
		return "no managed worktrees · /worktree new <name> makes one"
	}
	var sb strings.Builder
	for _, in := range infos {
		mark := " "
		if resolve(cwd) == resolve(in.Path) {
			mark = "*"
		}
		var notes []string
		if in.Missing {
			notes = append(notes, "directory missing")
		}
		if in.Dirty {
			notes = append(notes, "uncommitted changes")
		}
		if in.Commits > 0 {
			notes = append(notes, fmt.Sprintf("%d commit(s) ahead", in.Commits))
		}
		if len(notes) == 0 {
			notes = append(notes, "clean")
		}
		fmt.Fprintf(&sb, "%s %-24s %s · %s\n", mark, in.Branch, strings.Join(notes, ", "), in.Path)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// within reports whether path is dir or somewhere under it, so a session
// working in a subdirectory of a worktree keeps it too.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
