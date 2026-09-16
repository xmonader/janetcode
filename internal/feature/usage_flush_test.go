package feature

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A flush that fails leaves the counts dirty. Clearing the flag before
// the write landed made every later flush a silent no-op, so a transient
// failure at exit lost the whole session's counts without a word.
func TestFlushStaysDirtyWhenTheWriteFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs an unwritable directory, which root ignores")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "cfg")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	u := OpenUsage(sub)
	u.Note("doctor")
	// Make the directory unwritable so the temp file cannot be created.
	if err := os.Chmod(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) })
	if err := u.Flush(); err == nil {
		t.Fatal("a flush into an unwritable directory reported success")
	}
	if err := os.Chmod(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	// The retry must actually write: the counts were never given up.
	if err := u.Flush(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	back := OpenUsage(sub)
	if back.Count("doctor") != 1 {
		t.Fatalf("the count was lost: %d", back.Count("doctor"))
	}
}

// A clean flush clears the flag, so a second one writes nothing.
func TestFlushIsIdempotentAfterSuccess(t *testing.T) {
	dir := t.TempDir()
	u := OpenUsage(dir)
	u.Note("doctor")
	if err := u.Flush(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, usageFile)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err == nil {
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		// Nothing to write, so an unwritable directory is not a failure.
		if err := u.Flush(); err != nil {
			t.Fatalf("a second flush tried to write: %v", err)
		}
		_ = os.Chmod(dir, 0o700)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Error("a second flush rewrote the file")
	}
}
