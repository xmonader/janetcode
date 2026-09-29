package repomap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ChangedFiles names files the way the index does — relative to root,
// even when root is a subdirectory of the repository — and a rename's
// old path is not misread as an entry of its own.
func TestChangedFilesMatchTheIndexsPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sub/a.go", "package a\n")
	write("sub/long_old_name.go", "package a\n")
	write("other/b.go", "package b\n")
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "init")
	write("sub/a.go", "package a\n\nvar X = 1\n")
	gitRun(t, repo, "mv", "sub/long_old_name.go", "sub/renamed.go")
	write("sub/fresh/c.go", "package c\n")
	write("other/b.go", "package b\n\nvar Y = 1\n")

	root := filepath.Join(repo, "sub")
	got := ChangedFiles(context.Background(), root)
	sort.Strings(got)
	want := []string{"a.go", "fresh/c.go", "renamed.go"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("ChangedFiles = %q, want %q", got, want)
	}
}
