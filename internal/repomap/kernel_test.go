package repomap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// kernelRepo is a committed repository of n Go files that call each other,
// so the ranking has structure to order by.
func kernelRepo(t *testing.T, n int) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("package p\n\nfunc Func%d() int { return Func%d() }\n", i, (i*7+3)%n)
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("f%02d.go", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "init")
	return repo
}

// Two sessions on the same commit — here two checkouts in different
// directories, as an eval's workspaces are — get the same bytes, and the
// second gets them from the store without building an index.
func TestKernelIsByteIdenticalAcrossCheckouts(t *testing.T) {
	ctx := context.Background()
	a := kernelRepo(t, 45)
	b := t.TempDir()
	gitRun(t, b, "clone", "-q", a, ".")
	store := t.TempDir()

	textA, keyA := Kernel(ctx, &Cache{Root: a}, store)
	if textA == "" || keyA == "" {
		t.Fatalf("no kernel for a 45-file committed tree (key %q)", keyA)
	}
	if !strings.Contains(textA, "f") || strings.Contains(textA, a) {
		t.Fatalf("the kernel should list relative paths and never the checkout's own:\n%s", textA)
	}
	if strings.Contains(textA, "ranked for this request") {
		t.Fatalf("the kernel claims a request it was never given:\n%s", textA)
	}
	stored, _ := filepath.Glob(filepath.Join(store, "*"+keyA+".map"))
	if len(stored) != 1 {
		t.Fatalf("a clean tree's kernel was not stored: %v", stored)
	}

	textB, keyB := Kernel(ctx, &Cache{Root: b}, store)
	if keyB != keyA || textB != textA {
		t.Fatalf("another checkout of the same commit got different bytes:\nkey %s vs %s", keyA, keyB)
	}
	// Even a fresh render, without the store, has to agree: the store
	// guards against drift, it should not be hiding any.
	if fresh, _ := Kernel(ctx, &Cache{Root: b}, ""); fresh != textA {
		t.Fatalf("a fresh render differs from the stored one:\n%s\n---\n%s", fresh, textA)
	}
}

// A dirty tree reads a stored kernel as it is, and does not store one of
// its own: its uncommitted changes are not the commit's.
func TestKernelDirtyTreeIsNotStored(t *testing.T) {
	ctx := context.Background()
	repo := kernelRepo(t, 45)
	if err := os.WriteFile(filepath.Join(repo, "zz_new.go"), []byte("package p\n\nfunc Uncommitted() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	text, key := Kernel(ctx, &Cache{Root: repo}, store)
	if text == "" || key == "" {
		t.Fatal("a dirty tree should still get a kernel rendered now")
	}
	if got, _ := filepath.Glob(filepath.Join(store, "*.map")); len(got) != 0 {
		t.Fatalf("a dirty tree's render was stored under the commit: %v", got)
	}

	// Once one is stored for the commit, a dirty checkout reads it back
	// unchanged instead of rendering its own changes into it.
	if err := os.WriteFile(filepath.Join(store, kernelVersion+"-"+key+".map"), []byte("STORED"), 0o644); err != nil {
		t.Fatal(err)
	}
	if text, _ := Kernel(ctx, &Cache{Root: repo}, store); text != "STORED" {
		t.Fatalf("the stored kernel was not used: %q", text)
	}
}

func TestKernelSkipsSmallAndUncommittedTrees(t *testing.T) {
	ctx := context.Background()
	if text, _ := Kernel(ctx, &Cache{Root: kernelRepo(t, 10)}, t.TempDir()); text != "" {
		t.Fatalf("a tree small enough to be inlined got a kernel map:\n%s", text)
	}
	plain := t.TempDir()
	for i := 0; i < 45; i++ {
		os.WriteFile(filepath.Join(plain, fmt.Sprintf("f%02d.go", i)), []byte("package p\n"), 0o644)
	}
	if text, key := Kernel(ctx, &Cache{Root: plain}, t.TempDir()); text != "" || key != "" {
		t.Fatalf("a tree outside git has no commit to key on, got %q %q", key, text)
	}
	if text, _ := Kernel(ctx, nil, ""); text != "" {
		t.Fatal("a nil cache produced a kernel")
	}
}
