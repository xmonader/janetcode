package memory

import (
	"os"
	"runtime"
	"testing"
)

// A memory entry holds what the user said about themselves and their
// work, so a new one is readable by the user alone, whatever the umask.
func TestNewEntryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes only")
	}
	s := Open(t.TempDir(), t.TempDir())
	e, _, err := s.Put(Entry{Description: "the user prefers table-driven tests", Source: "user"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("%s is %v: a memory entry must not be readable by anyone else", e.Path, perm)
	}
}
