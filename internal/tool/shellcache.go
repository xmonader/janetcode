package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ShellCache returns the previous result of a command when it is run again
// on an unchanged tree. A model that reruns the full test suite after
// every thought is otherwise invisible to loop detection, because passing
// is not failing. The tree fingerprint is HEAD plus `git status` plus the
// size and mtime of every changed file, so any edit invalidates every
// entry; entries also expire so clock- or network-dependent commands are
// not served stale for long. Append `# no-cache` to a command to force a
// rerun.
type ShellCache struct {
	// TTL bounds how long a result is reused. Zero means 10 minutes.
	TTL time.Duration
	mu  sync.Mutex
	m   map[string]shellEntry
}

type shellEntry struct {
	out  string
	when time.Time
}

const noCacheMarker = "# no-cache"

func (c *ShellCache) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return 10 * time.Minute
}

// key returns the cache key for command on root, or "" when caching does
// not apply (not a git tree, or the command opts out).
func (c *ShellCache) key(root, command string) string {
	if c == nil || strings.Contains(command, noCacheMarker) {
		return ""
	}
	fp := treeFingerprint(root)
	if fp == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fp + "\x00" + strings.TrimSpace(command)))
	return hex.EncodeToString(sum[:])
}

// get returns a cached result and its age.
func (c *ShellCache) get(key string) (string, time.Duration, bool) {
	if c == nil || key == "" {
		return "", 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return "", 0, false
	}
	age := time.Since(e.when)
	if age > c.ttl() {
		delete(c.m, key)
		return "", 0, false
	}
	return e.out, age, true
}

func (c *ShellCache) put(key, out string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]shellEntry{}
	}
	c.m[key] = shellEntry{out: out, when: time.Now()}
}

// treeFingerprint identifies the working tree state, or "" outside git.
func treeFingerprint(root string) string {
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

// invalidate forgets every cached result; a background job may change the
// tree at any time, so nothing recorded before it is trustworthy.
func (c *ShellCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}
