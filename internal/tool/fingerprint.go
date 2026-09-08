package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// TreeFingerprint identifies the state of a directory tree. In a git
// repository it comes from HEAD plus the status of changed files; elsewhere
// from the size and modification time of every file, skipping hidden
// directories and dependency folders and giving up past 20k files.
func TreeFingerprint(root string) string {
	if fp := treeFingerprint(root); fp != "" {
		return fp
	}
	h := sha256.New()
	n := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "target" || name == "__pycache__") {
				return filepath.SkipDir
			}
			return nil
		}
		if n++; n > 20000 {
			return filepath.SkipAll
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(h, "%s|%d|%d\n", p, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return "walk:" + hex.EncodeToString(h.Sum(nil))
}
