package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIOCacheServesASecondRead(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("hello\n"), 0o644)
	cache := &IOCache{}
	r := ReadFile{Root: root, Cache: cache}
	if _, err := r.Run(context.Background(), raw(`{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if cache.Hits() != 0 {
		t.Fatalf("first read hit the cache")
	}
	if _, err := r.Run(context.Background(), raw(`{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if cache.Hits() != 1 {
		t.Fatalf("hits = %d", cache.Hits())
	}
	// A sibling tool sharing the cache is how a subagent reuses the
	// parent's reads.
	r2 := ReadFile{Root: root, Cache: cache}
	if _, err := r2.Run(context.Background(), raw(`{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if cache.Hits() != 2 {
		t.Fatalf("shared hits = %d", cache.Hits())
	}
}

func TestIOCacheDropsOnWrite(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("old\n"), 0o644)
	cache := &IOCache{}
	r := ReadFile{Root: root, Cache: cache}
	w := WriteFile{Root: root, Cache: cache}
	if _, err := r.Run(context.Background(), raw(`{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background(), raw(`{"path":"a.txt","content":"new\n"}`)); err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), raw(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cache.Hits() != 0 {
		t.Fatalf("write should drop the cache, hits=%d", cache.Hits())
	}
	if !strings.Contains(out, "new") {
		t.Fatalf("stale cache: %q", out)
	}
}

// The cache holds file content for the whole session and is shared with
// every subagent, so it has to have a budget: a fan-out over a large
// repository would otherwise make it the biggest thing in the process.
func TestIOCacheEvictsPastItsBudget(t *testing.T) {
	c := &IOCache{MaxBytes: 1000}
	mod := time.Now()
	data := make([]byte, 100)
	for i := 0; i < 50; i++ {
		c.Put(fmt.Sprintf("f%d", i), mod, int64(len(data)), data)
	}
	if got := c.Bytes(); got > 1000 {
		t.Fatalf("cache holds %d bytes for a 1000-byte budget", got)
	}
	// The newest entry survives; the oldest went first.
	if _, ok := c.Get("f49", mod, int64(len(data))); !ok {
		t.Fatal("the newest entry was evicted")
	}
	if _, ok := c.Get("f0", mod, int64(len(data))); ok {
		t.Fatal("the oldest entry survived a full cache")
	}
	// Dropping frees the budget it held.
	before := c.Bytes()
	c.Drop("f49")
	if c.Bytes() != before-100 {
		t.Fatalf("drop did not free its bytes: %d -> %d", before, c.Bytes())
	}
	// A file bigger than the whole budget is not cached, and does not
	// evict everything else to fail at it.
	c.Put("huge", mod, 2000, make([]byte, 2000))
	if _, ok := c.Get("huge", mod, 2000); ok {
		t.Fatal("a file larger than the budget was cached")
	}
	if c.Bytes() == 0 {
		t.Fatal("caching an oversized file emptied the cache")
	}
}

func TestIOCacheMissesWhenFileChangesOnDisk(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("one\n"), 0o644)
	cache := &IOCache{}
	r := ReadFile{Root: root, Cache: cache}
	if _, err := r.Run(context.Background(), raw(`{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	// A different length, so a same-size shortcut could not mask a stale
	// hit either.
	os.WriteFile(p, []byte("two is longer\n"), 0o644)
	out, err := r.Run(context.Background(), raw(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "two is longer") || strings.Contains(out, "one") {
		t.Fatalf("served stale bytes: %q", out)
	}
}

// changesAfterRead is a local host on which the file is rewritten the
// moment a read of it returns: a formatter or the user saving, landing
// between the read and whatever the reader does next.
type changesAfterRead struct {
	Local
	then []byte
	done bool
}

func (h *changesAfterRead) ReadFile(ctx context.Context, path string) ([]byte, error) {
	data, err := h.Local.ReadFile(ctx, path)
	if err == nil && !h.done {
		h.done = true
		os.WriteFile(path, h.then, 0o644)
	}
	return data, err
}

// The bytes a read got belong to the file as it was before the read, not
// to whatever is there once it returns. They used to be cached under the
// identity stat'ed after the read, so a file rewritten in between had its
// old content filed under its new mtime and size — and every later read
// served the old content as current, for as long as the file stayed put,
// while the stale-read check on disk kept refusing the edit made from it.
func TestIOCacheDoesNotFileOldBytesUnderANewIdentity(t *testing.T) {
	for _, tool := range []string{"read_file", "read_many_files"} {
		root := t.TempDir()
		p := filepath.Join(root, "a.txt")
		os.WriteFile(p, []byte("one\n"), 0o644)
		old := time.Now().Add(-time.Hour)
		os.Chtimes(p, old, old)
		ws := &Workspace{host: &changesAfterRead{Local: Local{root: root}, then: []byte("two is longer\n")}}
		cache := &IOCache{}
		var run func() (string, error)
		if tool == "read_file" {
			r := ReadFile{WS: ws, Cache: cache}
			run = func() (string, error) { return r.Run(context.Background(), raw(`{"path":"a.txt"}`)) }
		} else {
			r := ReadManyFiles{WS: ws, Cache: cache}
			run = func() (string, error) { return r.Run(context.Background(), raw(`{"paths":["a.txt"]}`)) }
		}
		if _, err := run(); err != nil {
			t.Fatal(err)
		}
		out, err := run()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "two is longer") {
			t.Fatalf("%s: served the pre-change bytes as current: %q", tool, out)
		}
	}
}
