package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
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
	fp := gitx.TreeState(root)
	if fp == "" {
		return ""
	}
	// The root is part of the key. The fingerprint is HEAD plus the
	// working-tree changes, and two checkouts of the same commit — a git
	// worktree, a second clone — produce exactly the same one, so a cache
	// shared between them would answer a question about this tree with an
	// answer computed in that one.
	sum := sha256.Sum256([]byte(root + "\x00" + fp + "\x00" + strings.TrimSpace(command)))
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

// maxShellEntries bounds the cache. Every entry holds a command's whole
// result, and a key is a tree state as well as a command, so a session
// that edits between runs accumulates one entry per run. Expiry alone
// does not bound it: entries are only looked at when their key comes up
// again, and a stale key never does.
const maxShellEntries = 200

func (c *ShellCache) put(key, out string) {
	if c == nil || key == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]shellEntry{}
	}
	if len(c.m) >= maxShellEntries {
		c.sweep()
	}
	c.m[key] = shellEntry{out: out, when: time.Now()}
}

// sweep drops expired entries, and if that was not enough, everything:
// what is left is by then mostly keys for tree states that no longer
// exist, and a cold cache costs one rerun of a command.
// Called with the lock held.
func (c *ShellCache) sweep() {
	for k, e := range c.m {
		if time.Since(e.when) > c.ttl() {
			delete(c.m, k)
		}
	}
	if len(c.m) >= maxShellEntries {
		c.m = map[string]shellEntry{}
	}
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
