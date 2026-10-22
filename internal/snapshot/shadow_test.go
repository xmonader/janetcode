package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestShadowSnapshotsADirectoryThatIsNotARepository: a shadow store
// snapshots a plain directory — edits, new files and deletions made by
// anything, a shell included — and restores it, without writing a byte
// into the directory itself.
func TestShadowSnapshotsADirectoryThatIsNotARepository(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "a1\n")
	write(t, filepath.Join(dir, "gone.txt"), "keep me\n")
	r, err := OpenShadow(ctx, dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !r.Shadow() {
		t.Fatal("a shadow store does not say so")
	}
	snap, err := r.Take(ctx, "before")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.txt"), "a2\n")
	write(t, filepath.Join(dir, "new.txt"), "new\n")
	os.Remove(filepath.Join(dir, "gone.txt"))
	if changed, err := r.Changed(ctx, snap); err != nil || !changed {
		t.Fatalf("changed = %v, %v", changed, err)
	}
	if _, err := r.Restore(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "a1\n" {
		t.Fatalf("a.txt = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "gone.txt")); string(b) != "keep me\n" {
		t.Fatalf("gone.txt = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a file created after the snapshot survived the restore")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the shadow store wrote a .git into the directory")
	}
	// Reopening finds the same store and the snapshot in it.
	r2, err := OpenShadow(ctx, dir, filepath.Dir(r.gitDir))
	if err != nil || r2.gitDir != r.gitDir {
		t.Fatalf("reopen = %v, %v", r2, err)
	}
	if moved, err := r2.Moved(ctx, snap); err != nil || moved {
		t.Fatalf("a shadow store's HEAD moved: %v, %v", moved, err)
	}
}

// TestShadowRefusesTooMuch: a tree past the bounds is not snapshotted on
// every turn.
func TestShadowRefusesTooMuch(t *testing.T) {
	dir := t.TempDir()
	big, err := os.Create(filepath.Join(dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(maxShadowBytes + 1); err != nil {
		t.Fatal(err)
	}
	big.Close()
	if _, err := OpenShadow(context.Background(), dir, t.TempDir()); !errors.Is(err, ErrShadowTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

// TestShadowRefusesAHomeThatIsASymlink: the directory is resolved before
// it is compared, so the home directory must be too, or a $HOME that is a
// link slips past the check.
func TestShadowRefusesAHomeThatIsASymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	t.Setenv("HOME", link)
	t.Setenv("USERPROFILE", link)
	if _, err := OpenShadow(context.Background(), real, t.TempDir()); !errors.Is(err, ErrShadowTooLarge) {
		t.Fatalf("OpenShadow(home) = %v, want ErrShadowTooLarge", err)
	}
}
