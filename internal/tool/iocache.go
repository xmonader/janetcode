package tool

import (
	"sync"
	"time"
)

// IOCache is a content cache shared by the parent agent and its
// subagents: the first read of a path pays disk, later reads with the
// same mtime and size reuse the bytes. Writes must Drop the path.
// A nil *IOCache disables caching.
//
// It is bounded. A session that reads its way through a large repository
// — or a subagent fan-out that reads a thousand files each — would
// otherwise hold every byte it ever read for as long as the session
// lives, and the cache would be the largest thing in the process. Past
// the budget the oldest entries go first, which is the right order for a
// cache whose hits come from a file being read again soon after.
type IOCache struct {
	// MaxBytes is the budget; zero means DefaultCacheBytes.
	MaxBytes int64

	mu    sync.Mutex
	files map[string]ioEntry
	// order is the paths in insertion order, for eviction. A path that
	// has been dropped or re-added leaves a stale entry behind, which is
	// skipped when it comes up.
	order []string
	bytes int64
	hits  int
}

// DefaultCacheBytes is how much file content one session keeps.
const DefaultCacheBytes = 64 << 20

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
	budget := c.MaxBytes
	if budget <= 0 {
		budget = DefaultCacheBytes
	}
	if int64(len(data)) > budget {
		// One file larger than the whole budget is not worth evicting
		// everything else for.
		return
	}
	if old, ok := c.files[path]; ok {
		c.bytes -= int64(len(old.data))
	}
	stored := make([]byte, len(data))
	copy(stored, data)
	c.files[path] = ioEntry{mod: mod, size: size, data: stored}
	c.order = append(c.order, path)
	c.bytes += int64(len(stored))
	for c.bytes > budget && len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		if oldest == path {
			continue // the entry just stored: its stale order entry
		}
		if e, ok := c.files[oldest]; ok {
			c.bytes -= int64(len(e.data))
			delete(c.files, oldest)
		}
	}
}

// Drop forgets a path after a write or delete.
func (c *IOCache) Drop(path string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if e, ok := c.files[path]; ok {
		c.bytes -= int64(len(e.data))
		delete(c.files, path)
	}
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

// Bytes is how much file content the cache is holding.
func (c *IOCache) Bytes() int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}
