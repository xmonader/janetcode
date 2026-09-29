package present

import (
	"path/filepath"
	"testing"
)

// Home is written as ~ only for home itself and what is under it: a
// sibling directory whose name starts with the user's own came out as
// "~2/x" for /home/me2/x.
func TestRelPathHomeIsADirectoryNotAPrefix(t *testing.T) {
	home := filepath.Join(t.TempDir(), "me")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cwd := filepath.Join(home, "proj")
	if got, want := RelPath(cwd, filepath.Join(home, "notes", "a.md")), "~"+string(filepath.Separator)+filepath.Join("notes", "a.md"); got != want {
		t.Fatalf("under home: %q, want %q", got, want)
	}
	other := home + "2" + string(filepath.Separator) + "x"
	if got := RelPath(cwd, other); got != other {
		t.Fatalf("a sibling of home: %q, want it unchanged", got)
	}
}
