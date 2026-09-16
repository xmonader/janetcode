package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Everything a session records is a transcript of someone's work, so
// none of it may be world-readable through a loose umask. This walks the
// whole set of files a session writes rather than each write site, so a
// new sidecar is caught by the policy rather than by review.
func TestEverySessionFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	// A path New has to create itself, so its own mode is under test too.
	dir := filepath.Join(t.TempDir(), "sessions")
	s, err := New(dir, t.TempDir(), "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(Event{Type: TypeAssistant, Output: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTitle("a title"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rate(0, RateUp, "good"); err != nil {
		t.Fatal(err)
	}
	if err := s.WritePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 {
		t.Fatalf("only %d files written; the test is not exercising the sidecars", len(entries))
	}
	for _, e := range entries {
		st, err := os.Stat(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s is %v: a session file must not be readable by anyone else", e.Name(), perm)
		}
	}
	// And the directory New created to hold them.
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("the sessions directory is %v", perm)
	}
}
