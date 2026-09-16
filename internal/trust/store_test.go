package trust

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentApprovalsDoNotLoseOneAnother(t *testing.T) {
	ts := Store{Path: filepath.Join(t.TempDir(), "trust.json")}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := ts.Trust(HooksKey(string(rune('a'+i))), "h"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 16; i++ {
		if !ts.Trusted(HooksKey(string(rune('a'+i))), "h") {
			t.Fatalf("approval %d was lost to a concurrent write", i)
		}
	}
}

func TestTrustReplacesTheStoreInOneStep(t *testing.T) {
	dir := t.TempDir()
	ts := Store{Path: filepath.Join(dir, "trust.json")}
	if err := ts.Trust("/a", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := ts.Trust("/b", "h2"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("a temporary file was left behind: %s", e.Name())
		}
	}
	info, err := os.Stat(ts.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("trust store is group/world readable: %v", perm)
	}
	if !ts.Trusted("/a", "h1") || !ts.Trusted("/b", "h2") {
		t.Fatal("the second write dropped the first approval")
	}
}

func TestACorruptStoreTrustsNothing(t *testing.T) {
	ts := Store{Path: filepath.Join(t.TempDir(), "trust.json")}
	if err := os.WriteFile(ts.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ts.Trusted("/a", "h1") {
		t.Fatal("a corrupt store must trust nothing")
	}
	if err := ts.Trust("/a", "h1"); err == nil {
		t.Fatal("writing over a corrupt store should report it rather than silently discard it")
	}
}

func TestANullStoreIsEmptyNotNil(t *testing.T) {
	ts := Store{Path: filepath.Join(t.TempDir(), "trust.json")}
	if err := os.WriteFile(ts.Path, []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ts.Trust("/a", "h1"); err != nil {
		t.Fatal(err)
	}
	if !ts.Trusted("/a", "h1") {
		t.Fatal("approval after a null store was lost")
	}
}
