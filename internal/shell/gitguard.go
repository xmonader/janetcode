package shell

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The workspace profile leaves the project's .git writable, because a
// sandboxed command that cannot commit, check out or fetch is not much
// use. But three things in there run code later, outside any sandbox,
// the next time the person types a git command: a hook, a config key
// that names a program (core.hooksPath, core.fsmonitor, core.sshCommand,
// an alias, a filter or diff driver, include.path, …), and the .git
// entry itself, which a gitdir: line can point at a directory full of
// both. Submodules have their own of each, under .git/modules. So a
// sandboxed command is bracketed: GitGuard records those before it runs
// and puts back whatever it changed afterwards.
//
// Config is judged key by key rather than byte for byte, because some
// ordinary git operations do write it — `git push -u`, `git checkout
// --track`, `git remote add`, `git config user.email`. Changes confined
// to the keys in safeGitKeys stand; any other key changed, added or
// removed puts the whole file back as it was.
//
// It is the enforcement Landlock can have at all (Landlock grants are
// inherited by everything beneath the project root, so it cannot carve
// .git/hooks out), and a backstop for docker and seatbelt, which protect
// .git/hooks with a read-only mount and a deny rule but see only the
// .git directory at the root, not a worktree's gitdir elsewhere, nor a
// config key.

// maxHookBytes bounds what one snapshot holds in memory. Hooks are small
// scripts; a hooks directory beyond this is not snapshotted.
const maxHookBytes = 8 << 20

type fileSnap struct {
	data []byte
	mode fs.FileMode
	link bool // a symlink: kept if it was there, removed if it is new
}

// gitDirSnap is one git directory's hooks and config: the repository's
// own, or a submodule's under modules/.
type gitDirSnap struct {
	dir     string
	hooks   map[string]fileSnap
	big     bool
	configs map[string]*fileSnap // nil value: the file did not exist
	// freshModule marks a submodule git directory the command created:
	// its sample hooks are allowed to appear.
	freshModule bool
}

// GitGuard is one command's before-picture of the parts of a repository
// that run code later.
type GitGuard struct {
	dotGit     string // the .git entry at the top of the work tree
	dotGitFile []byte // its content when it is a file (a worktree), else nil
	dotGitDir  bool
	common     string // the common git directory (modules/ lives here)
	dirs       map[string]*gitDirSnap
}

// SnapshotGit records the repository root sits in. It returns nil when
// root is not inside a git work tree, and Restore on nil does nothing.
func SnapshotGit(root string) *GitGuard {
	dotGit := findDotGit(root)
	if dotGit == "" {
		return nil
	}
	g := &GitGuard{dotGit: dotGit, dirs: map[string]*gitDirSnap{}}
	fi, err := os.Lstat(dotGit)
	if err != nil {
		return nil
	}
	gitDir := dotGit
	switch {
	case fi.IsDir():
		g.dotGitDir = true
	case fi.Mode().IsRegular():
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return nil
		}
		g.dotGitFile = data
		line := strings.TrimSpace(strings.SplitN(string(data), "\n", 2)[0])
		p, ok := strings.CutPrefix(line, "gitdir:")
		if !ok {
			return nil
		}
		p = strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(dotGit), p)
		}
		gitDir = filepath.Clean(p)
	default:
		return nil
	}
	g.common = gitDir
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		c := strings.TrimSpace(string(data))
		if !filepath.IsAbs(c) {
			c = filepath.Join(gitDir, c)
		}
		g.common = filepath.Clean(c)
	}
	g.dirs[g.common] = snapGitDir(g.common, filepath.Join(gitDir, "config.worktree"))
	for _, m := range moduleGitDirs(g.common) {
		g.dirs[m] = snapGitDir(m)
	}
	return g
}

func snapGitDir(dir string, extraConfigs ...string) *gitDirSnap {
	s := &gitDirSnap{dir: dir, hooks: map[string]fileSnap{}, configs: map[string]*fileSnap{}}
	for _, c := range append([]string{filepath.Join(dir, "config")}, extraConfigs...) {
		if data, err := os.ReadFile(c); err == nil {
			mode := fs.FileMode(0o644)
			if st, err := os.Stat(c); err == nil {
				mode = st.Mode().Perm()
			}
			s.configs[c] = &fileSnap{data: data, mode: mode}
		} else {
			s.configs[c] = nil
		}
	}
	total := 0
	hooks := filepath.Join(dir, "hooks")
	_ = filepath.WalkDir(hooks, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || s.big {
			return nil
		}
		rel, _ := filepath.Rel(hooks, p)
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !info.Mode().IsRegular() {
			s.hooks[rel] = fileSnap{link: true}
			return nil
		}
		total += int(info.Size())
		if total > maxHookBytes {
			s.big = true
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		s.hooks[rel] = fileSnap{data: data, mode: info.Mode().Perm()}
		return nil
	})
	return s
}

// moduleGitDirs lists the submodule git directories under common/modules,
// nested ones included: each is a directory holding a HEAD file.
func moduleGitDirs(common string) []string {
	var out []string
	root := filepath.Join(common, "modules")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == root {
			return nil
		}
		switch d.Name() {
		case "objects", "refs", "logs", "hooks", "info", "lfs":
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(p, "HEAD")); err == nil {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// findDotGit walks up from dir to the .git entry of the work tree it is
// in, "" when there is none.
func findDotGit(dir string) string {
	dir = filepath.Clean(dir)
	for {
		p := filepath.Join(dir, ".git")
		if _, err := os.Lstat(p); err == nil {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Restore puts back what the command changed and returns a note for the
// command's output, "" when it changed nothing guarded.
func (g *GitGuard) Restore() string {
	if g == nil {
		return ""
	}
	var reverted []string
	// The .git entry first: everything else is found through it.
	if fi, err := os.Lstat(g.dotGit); err == nil {
		switch {
		case g.dotGitFile != nil:
			data, rerr := os.ReadFile(g.dotGit)
			if !fi.Mode().IsRegular() || rerr != nil || !bytes.Equal(data, g.dotGitFile) {
				_ = os.RemoveAll(g.dotGit)
				if os.WriteFile(g.dotGit, g.dotGitFile, 0o644) == nil {
					reverted = append(reverted, ".git (it pointed somewhere else)")
				}
			}
		case g.dotGitDir && !fi.IsDir():
			// A directory replaced by a file or a link: that is what git
			// would follow. The directory itself is gone and cannot be
			// conjured back, so the pointer is removed and the note says so.
			_ = os.Remove(g.dotGit)
			reverted = append(reverted, ".git (was replaced by a link or file, now removed; the repository directory it replaced is gone)")
		}
	}
	for _, s := range g.dirs {
		reverted = append(reverted, s.restore(g.common)...)
	}
	// A submodule added by the command brings a git directory the
	// snapshot never saw. Its sample hooks are inert; anything else in
	// its hooks, and any key in its config beyond the safe ones, is not.
	for _, m := range moduleGitDirs(g.common) {
		if _, seen := g.dirs[m]; seen {
			continue
		}
		fresh := &gitDirSnap{dir: m, hooks: map[string]fileSnap{}, configs: map[string]*fileSnap{filepath.Join(m, "config"): nil}, freshModule: true}
		reverted = append(reverted, fresh.restore(g.common)...)
	}
	if len(reverted) == 0 {
		return ""
	}
	sort.Strings(reverted)
	return "[sandbox: put back " + strings.Join(reverted, "; ") + ". A sandboxed command may not change what git runs later outside the sandbox; if the change was meant, the person can make it with the sandbox off]"
}

// restore puts one git directory back and names what it put back,
// relative to the common directory.
func (s *gitDirSnap) restore(common string) []string {
	var out []string
	name := func(p string) string {
		if rel, err := filepath.Rel(common, p); err == nil && !strings.HasPrefix(rel, "..") {
			return ".git/" + filepath.ToSlash(rel)
		}
		return p
	}
	for path, before := range s.configs {
		if why := configChange(path, before, s.freshModule); why != "" {
			if before == nil {
				_ = os.Remove(path)
			} else {
				_ = os.WriteFile(path, before.data, before.mode)
			}
			out = append(out, name(path)+" ("+why+")")
		}
	}
	if s.big {
		return out
	}
	hooks := filepath.Join(s.dir, "hooks")
	if fi, err := os.Lstat(hooks); err == nil && !fi.IsDir() {
		_ = os.Remove(hooks)
		out = append(out, name(hooks)+" (was replaced by a link or file)")
	}
	now := map[string]bool{}
	_ = filepath.WalkDir(hooks, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(hooks, p)
		now[rel] = true
		before, had := s.hooks[rel]
		if !had {
			if s.freshModule && strings.HasSuffix(rel, ".sample") && d.Type().IsRegular() {
				return nil
			}
			_ = os.Remove(p)
			out = append(out, name(p)+" (added)")
			return nil
		}
		if before.link {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		data, _ := os.ReadFile(p)
		if !info.Mode().IsRegular() || !bytes.Equal(data, before.data) || info.Mode().Perm() != before.mode {
			_ = os.Remove(p)
			_ = os.WriteFile(p, before.data, before.mode)
			_ = os.Chmod(p, before.mode)
			out = append(out, name(p)+" (changed)")
		}
		return nil
	})
	for rel, before := range s.hooks {
		if now[rel] || before.link {
			continue
		}
		p := filepath.Join(hooks, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if os.WriteFile(p, before.data, before.mode) == nil {
			_ = os.Chmod(p, before.mode)
			out = append(out, name(p)+" (removed)")
		}
	}
	return out
}

// configChange reports why the config file at path, as it is now, may
// not stand: "" when it is unchanged or its changes are all to keys a
// sandboxed command may set.
func configChange(path string, before *fileSnap, _ bool) string {
	data, err := os.ReadFile(path)
	if err != nil {
		if before == nil {
			return ""
		}
		return "it was removed"
	}
	var old []byte
	if before != nil {
		if bytes.Equal(data, before.data) {
			return ""
		}
		old = before.data
	}
	a, okA := parseGitConfig(old)
	b, okB := parseGitConfig(data)
	if !okA || !okB {
		return "it no longer parses as a git config"
	}
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var bad []string
	for k := range keys {
		if strings.Join(a[k], "\x00") == strings.Join(b[k], "\x00") {
			continue
		}
		if !safeGitKey(k) {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return strings.Join(bad, ", ") + " changed"
}

// safeGitKeys are the config keys ordinary git operations write and that
// name no program: tracking (push -u, checkout --track), remotes (remote
// add, set-url), identity, and a few plain preferences. A "*" stands for
// a subsection name (a branch or remote).
var safeGitKeys = map[string]bool{
	"branch.*.remote": true, "branch.*.merge": true, "branch.*.rebase": true,
	"branch.*.pushremote": true, "branch.*.description": true,
	"remote.*.url": true, "remote.*.pushurl": true, "remote.*.fetch": true,
	"remote.*.push": true, "remote.*.tagopt": true, "remote.*.prune": true,
	"remote.*.mirror": true, "remote.*.skipfetchall": true, "remote.*.promisor": true,
	"remote.*.partialclonefilter": true,
	"submodule.*.url":             true, "submodule.*.active": true, "submodule.*.branch": true,
	"user.name": true, "user.email": true,
	"core.repositoryformatversion": true, "core.filemode": true, "core.bare": true,
	"core.logallrefupdates": true, "core.ignorecase": true, "core.precomposeunicode": true,
	"core.symlinks": true, "core.worktree": true, "core.autocrlf": true, "core.eol": true, "core.sparsecheckout": true,
	"extensions.worktreeconfig": true, "extensions.objectformat": true,
	"init.defaultbranch": true, "pull.rebase": true, "pull.ff": true,
	"push.default": true, "push.autosetupremote": true, "fetch.prune": true,
	"rerere.enabled": true, "gc.auto": true, "index.version": true,
}

func safeGitKey(k string) bool {
	if safeGitKeys[k] {
		return true
	}
	parts := strings.Split(k, ".")
	if len(parts) >= 3 {
		return safeGitKeys[parts[0]+".*."+parts[len(parts)-1]]
	}
	if len(parts) == 2 && (parts[0] == "advice" || parts[0] == "color") {
		return true
	}
	return false
}

// parseGitConfig reads a git config file into "section.subsection.key"
// → values, with section and key lowercased as git compares them and the
// subsection kept as written. It is strict: a line it cannot read makes
// ok false, and the caller treats that as a change it cannot vouch for.
func parseGitConfig(data []byte) (map[string][]string, bool) {
	out := map[string][]string{}
	section := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	pending := ""
	for sc.Scan() {
		line := pending + sc.Text()
		pending = ""
		if strings.HasSuffix(line, "\\") && !strings.HasSuffix(line, "\\\\") {
			pending = strings.TrimSuffix(line, "\\")
			continue
		}
		t := strings.TrimSpace(line)
		if t == "" || t[0] == '#' || t[0] == ';' {
			continue
		}
		if t[0] == '[' {
			end := strings.LastIndex(t, "]")
			if end < 0 {
				return nil, false
			}
			rest := strings.TrimSpace(t[end+1:])
			if rest != "" && rest[0] != '#' && rest[0] != ';' {
				// "[core] hooksPath = x" on one line: git reads the key.
				// Rather than parse it, refuse to vouch for it.
				return nil, false
			}
			inner := strings.TrimSpace(t[1:end])
			if name, sub, ok := strings.Cut(inner, " "); ok {
				sub = strings.TrimSpace(sub)
				if len(sub) < 2 || sub[0] != '"' || sub[len(sub)-1] != '"' {
					return nil, false
				}
				section = strings.ToLower(name) + "." + strings.ReplaceAll(sub[1:len(sub)-1], `\"`, `"`)
			} else if name, sub, ok := strings.Cut(inner, "."); ok {
				section = strings.ToLower(name) + "." + strings.ToLower(sub)
			} else {
				section = strings.ToLower(inner)
			}
			continue
		}
		if section == "" {
			return nil, false
		}
		key, val, hasVal := strings.Cut(t, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || strings.ContainsAny(key, " \t\"") {
			return nil, false
		}
		v := "true"
		if hasVal {
			v = strings.TrimSpace(val)
		}
		full := section + "." + key
		out[full] = append(out[full], v)
	}
	if sc.Err() != nil || pending != "" {
		return nil, false
	}
	return out, true
}

// String is for debugging and tests.
func (g *GitGuard) String() string {
	if g == nil {
		return "<no repository>"
	}
	return fmt.Sprintf("guard %s: %d git directories", g.dotGit, len(g.dirs))
}
