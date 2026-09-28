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
		// git status refreshes the index and writes it back under
		// index.lock when it can; run this often in the background and a
		// commit the user makes meanwhile fails on the lock. Optional
		// locks off is git's own setting for exactly this kind of caller.
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.Output()
		if err != nil {
			return "", false
		}
		return string(out), true
	}
	// The top level comes with HEAD because status paths are relative to
	// it, not to root: a session rooted in a subdirectory would otherwise
	// stat paths that do not exist, and a second edit to a file already
	// modified — same status line, new size and time — would leave the
	// fingerprint as it was.
	out, ok := git("rev-parse", "--show-toplevel", "HEAD")
	if !ok {
		return ""
	}
	top, head, ok := strings.Cut(out, "\n")
	if !ok || top == "" {
		return ""
	}
	status, ok := git("status", "--porcelain", "-z", "--untracked-files=all")
	if !ok {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(head)
	entries := strings.Split(status, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		sb.WriteString(entry)
		p := filepath.Join(top, filepath.FromSlash(entry[3:]))
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&sb, "|%d|%d", st.Size(), st.ModTime().UnixNano())
		}
		sb.WriteByte('\n')
		// A rename or copy is followed by its source path as an entry of
		// its own, with no status columns to cut off.
		if strings.ContainsAny(entry[:2], "RC") && i+1 < len(entries) {
			i++
			sb.WriteString(entries[i])
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
