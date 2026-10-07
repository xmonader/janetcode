package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenSnapshotsGivesAPlainDirectoryAShadowStore: outside git the
// session still gets whole-tree snapshots for /undo, kept in the data
// directory beside the sessions, never in the project.
func TestOpenSnapshotsGivesAPlainDirectoryAShadowStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	repo := openSnapshots(dir, filepath.Join(data, "sessions"))
	if repo == nil || !repo.Shadow() {
		t.Fatalf("repo = %+v", repo)
	}
	if _, err := os.Stat(filepath.Join(data, "shadow")); err != nil {
		t.Fatalf("the shadow store is not beside the sessions: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Fatal("the project got a .git")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if r := openSnapshots(home, filepath.Join(data, "sessions")); r != nil && r.Shadow() {
			t.Fatal("a home directory got a shadow store")
		}
	}
}
