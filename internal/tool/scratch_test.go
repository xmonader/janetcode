package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScratchDirIsOutsideTheRepoAndGoneAtTheEnd(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	dir, remove, err := OpenScratch("sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(dir) || strings.HasPrefix(dir, repo) || !IsScratchPath(filepath.Join(dir, "check.py")) {
		t.Fatalf("dir = %q", dir)
	}
	if IsScratchPath(filepath.Join(repo, "check.py")) {
		t.Fatal("a repo path read as scratch")
	}
	// An id that climbs stays inside.
	if d, rm, err := OpenScratch("../../x"); err != nil || !IsScratchPath(d+"/f") {
		t.Fatalf("climbing id: %q %v", d, err)
	} else {
		rm()
	}

	// Without the extra root the edit tools refuse the path; with it they
	// write and edit there, and the repo stays untouched.
	ws := NewLocal(repo)
	p := filepath.Join(dir, "check.py")
	w := WriteFile{Root: repo, WS: ws}
	if _, err := w.Run(context.Background(), raw(`{"path":"`+p+`","content":"print(1)\n"}`)); err == nil {
		t.Fatal("wrote outside the workspace without the extra root")
	}
	if err := ws.AddDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Run(context.Background(), raw(`{"path":"`+p+`","content":"print(1)\n"}`)); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	e := EditFile{Root: repo, WS: ws}
	if _, err := e.Run(context.Background(), raw(`{"path":"`+p+`","old_string":"print(1)","new_string":"print(2)"}`)); err != nil {
		t.Fatalf("edit_file: %v", err)
	}
	if got, _ := os.ReadFile(p); string(got) != "print(2)\n" {
		t.Fatalf("file = %q", got)
	}
	if ents, _ := os.ReadDir(repo); len(ents) != 0 {
		t.Fatalf("the repo gained files: %v", ents)
	}

	remove()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir still there: %v", err)
	}
}
