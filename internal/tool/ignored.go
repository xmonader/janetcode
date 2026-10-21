package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/ignore"
)

// The project's AI ignore files (.aiignore and friends; see package
// ignore) keep paths away from the file tools: read, write, edit, list,
// glob and grep. They are a statement of what the agent should not look
// at, not a sandbox: bash can still cat what read_file refuses, the
// same way a .gitignore does not stop anyone reading a file. Remote
// workspaces are not covered: their ignore files are on the other host.

// ignoredErr is the refusal for abs, or nil.
func ignoredErr(roots []string, abs, named string) error {
	root := rootContaining(roots, abs)
	if root == "" {
		return nil
	}
	isDir := false
	if fi, err := os.Stat(abs); err == nil {
		isDir = fi.IsDir()
	}
	if hit, src := ignore.For(root).Excluded(abs, isDir); hit {
		return fmt.Errorf("%s is excluded from the agent's file tools by %s; leave it alone, or ask the user to change that file", named, src)
	}
	// A symlink is judged by what it names too: `config.txt -> .env` or
	// `notsecret -> secrets` under a name no rule matches read and
	// overwrote exactly what the rules keep away.
	if real, ok := realUnder(roots, abs); ok {
		if hit, src := ignore.For(real.root).Excluded(real.path, isDir); hit {
			return fmt.Errorf("%s leads to %s, which is excluded from the agent's file tools by %s; leave it alone, or ask the user to change that file", named, displayPath(real.root, real.path), src)
		}
	}
	return nil
}

// rootedPath is a path and the root it lies under.
type rootedPath struct{ root, path string }

// realUnder resolves the symlinks on abs (those of its deepest existing
// ancestor, for a path not yet created) and, when that lands somewhere
// else under one of roots, returns it spelled from that root — a root
// reached through a symlink of its own (macOS's /var) is resolved too, so
// the rules are matched against the root's own spelling. ok is false when
// nothing on the path is a link, or it leads outside every root.
func realUnder(roots []string, abs string) (rootedPath, bool) {
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if real, err = resolveExisting(abs); err != nil {
			return rootedPath{}, false
		}
	}
	for _, r := range roots {
		if r == "" {
			continue
		}
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			rr = r
		}
		rel, err := filepath.Rel(rr, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		p := filepath.Join(r, rel)
		if p == filepath.Clean(abs) {
			return rootedPath{}, false
		}
		return rootedPath{root: r, path: p}, true
	}
	return rootedPath{}, false
}

func rootContaining(roots []string, abs string) string {
	for _, r := range roots {
		if r == "" {
			continue
		}
		rel, err := filepath.Rel(r, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return r
		}
	}
	return ""
}

// walkIgnored reports whether a walk under root should skip abs.
func walkIgnored(root, abs string, isDir bool) bool {
	if root == "" {
		return false
	}
	hit, _ := ignore.For(root).Excluded(abs, isDir)
	return hit
}

// ignoreRoot is the root whose ignore files govern dir: the working
// directory or the extra directory dir is in; "" for a remote workspace,
// whose ignore files are on the other host.
func ignoreRoot(ws *Workspace, root, dir string) string {
	if ws != nil && !ws.Local() {
		return ""
	}
	roots := []string{rootOf(root, ws)}
	if ws != nil {
		roots = append(roots, ws.ExtraDirs()...)
	}
	return rootContaining(roots, dir)
}
