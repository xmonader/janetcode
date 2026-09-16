package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// treeStateTimeout bounds the two git commands the fingerprint runs.
const treeStateTimeout = 10 * time.Second

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
	// Bounded, because this runs before every bash call (the shell
	// cache's key) and before every repo-map rebuild: a git that cannot
	// finish — a lock another process holds, a network filesystem, a
	// repository with a hook wedged on stdin — would otherwise hang the
	// tool call that asked. A timeout means "no fingerprint", which
	// callers already handle as "do not cache".
	ctx, cancel := context.WithTimeout(context.Background(), treeStateTimeout)
	defer cancel()
	git := func(args ...string) (string, bool) {
		cmd := exec.CommandContext(ctx, "git", args...)
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
