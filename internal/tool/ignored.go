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
	return nil
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
