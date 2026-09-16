package tool

import (
	"context"
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
