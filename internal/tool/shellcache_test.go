package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellCacheReplaysOnUnchangedTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("1"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")

	b := Bash{Root: root, Cache: &ShellCache{}}
	counter := `{"command":"echo run >> count.log; wc -l < count.log"}`
	first, err := b.Run(context.Background(), raw(counter))
	if err != nil {
		t.Fatal(err)
	}
	// count.log is a new untracked file: the tree changed, so a rerun is
	// real. Then the tree is stable and the third call is served from cache.
	second, _ := b.Run(context.Background(), raw(counter))
	if strings.Contains(second, "[cached") || strings.TrimSpace(strings.Split(second, "\n")[0]) != "2" {
		t.Fatalf("second = %q (first %q)", second, first)
	}
	stable := `{"command":"cat a.txt"}`
	b.Run(context.Background(), raw(stable))
	third, _ := b.Run(context.Background(), raw(stable))
	if !strings.Contains(third, "[cached") || !strings.HasPrefix(third, "1") {
		t.Fatalf("third = %q", third)
	}
	// Editing a file invalidates; the opt-out marker bypasses.
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("2"), 0o644)
	if out, _ := b.Run(context.Background(), raw(stable)); strings.Contains(out, "[cached") || !strings.HasPrefix(out, "2") {
		t.Fatalf("after edit = %q", out)
	}
	if out, _ := b.Run(context.Background(), raw(`{"command":"cat a.txt # no-cache"}`)); strings.Contains(out, "[cached") {
		t.Fatalf("no-cache = %q", out)
	}
	// Outside git nothing is cached.
	plain := Bash{Root: t.TempDir(), Cache: &ShellCache{}}
	plain.Run(context.Background(), raw(`{"command":"echo x"}`))
	if out, _ := plain.Run(context.Background(), raw(`{"command":"echo x"}`)); strings.Contains(out, "[cached") {
		t.Fatalf("non-git = %q", out)
	}
}

// Two worktrees of the same commit have the same fingerprint — HEAD plus
// a clean status is all there is to one — so with the root left out of
// the key a cache shared between them served the first worktree's output
// as the answer for the second.
func TestShellCacheKeysAreNotSharedBetweenWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(root, "init", "-q")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("1"), 0o644)
	run(root, "add", ".")
	run(root, "commit", "-q", "-m", "init")
	other := filepath.Join(t.TempDir(), "wt")
	run(root, "worktree", "add", "-q", "-b", "second", other)

	c := &ShellCache{}
	command := "go test ./..."
	a, b := c.key(root, command), c.key(other, command)
	if a == "" || b == "" {
		t.Fatalf("no key: %q %q", a, b)
	}
	if a == b {
		t.Fatal("two worktrees share a cache key; one's output would be served as the other's")
	}
}

// The cache is keyed by tree state as well as command, so a session that
// edits between runs makes a new key every time. Nothing ever looks at an
// old key again, so expiry alone does not bound the cache.
func TestShellCacheIsBounded(t *testing.T) {
	c := &ShellCache{}
	for i := 0; i < 5*maxShellEntries; i++ {
		c.put(fmt.Sprintf("key-%d", i), strings.Repeat("x", 100))
	}
	c.mu.Lock()
	n := len(c.m)
	c.mu.Unlock()
	if n > maxShellEntries {
		t.Fatalf("cache holds %d entries, cap is %d", n, maxShellEntries)
	}
	// It still serves what it was just given.
	c.put("fresh", "output")
	if out, _, ok := c.get("fresh"); !ok || out != "output" {
		t.Fatalf("a fresh entry was not served: %q %v", out, ok)
	}
}
