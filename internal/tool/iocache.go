package tool

import (
	"sync"
	"time"
)

// IOCache is a content cache shared by the parent agent and its
// subagents: the first read of a path pays disk, later reads with the
// same mtime and size reuse the bytes. Writes must Drop the path.
// A nil *IOCache disables caching.
type IOCache struct {
	mu    sync.Mutex
	files map[string]ioEntry
	hits  int
}

type ioEntry struct {
	mod  time.Time
	size int64
	data []byte
}

// Get returns a copy of the cached bytes when the on-disk identity still
// matches.
func (c *IOCache) Get(path string, mod time.Time, size int64) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.files[path]
	if !ok || e.size != size || !e.mod.Equal(mod) {
		return nil, false
	}
	c.hits++
	out := make([]byte, len(e.data))
	copy(out, e.data)
	return out, true
}

// Put stores bytes under path.
func (c *IOCache) Put(path string, mod time.Time, size int64, data []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.files == nil {
		c.files = map[string]ioEntry{}
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	c.files[path] = ioEntry{mod: mod, size: size, data: stored}
}

// Drop forgets a path after a write or delete.
func (c *IOCache) Drop(path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.files, path)
	c.mu.Unlock()
}

// Hits is how many times Get served bytes without disk.
func (c *IOCache) Hits() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits
}
