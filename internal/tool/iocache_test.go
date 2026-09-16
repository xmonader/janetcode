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
	r.Run(context.Background(), raw(`{"path":"a.txt"}`))
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(p, []byte("two\n"), 0o644)
	out, _ := r.Run(context.Background(), raw(`{"path":"a.txt"}`))
	if strings.Contains(out, "one") && !strings.Contains(out, "two") {
		t.Fatalf("served stale bytes: %q", out)
	}
}
