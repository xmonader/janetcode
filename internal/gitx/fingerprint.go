package gitx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// TreeFingerprint identifies the state of a directory tree. In a git
// repository it comes from HEAD plus the status of changed files; elsewhere
// from the size and modification time of every file, skipping hidden
// directories and dependency folders and giving up past 20k files.
func TreeFingerprint(root string) string {
	if fp := TreeState(root); fp != "" {
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

// TreeState identifies the working tree state: HEAD plus the status,
// size and modification time of every changed file. It is "" outside git.
func TreeState(root string) string {
	git := func(args ...string) (string, bool) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return "", false
		}
		return string(out), true
	}
	head, ok := git("rev-parse", "HEAD")
	if !ok {
		return ""
	}
	status, ok := git("status", "--porcelain", "-z", "--untracked-files=all")
	if !ok {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(head)
	for _, entry := range strings.Split(status, "\x00") {
		if len(entry) < 4 {
			continue
		}
		sb.WriteString(entry)
		p := filepath.Join(root, entry[3:])
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&sb, "|%d|%d", st.Size(), st.ModTime().UnixNano())
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
