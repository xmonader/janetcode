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

// The project directory and home are taken out of a command only where a
// path starts: the same letters in the middle of another path are part
// of that path. With cwd /a/b, "cat /x/a/b/c" was shown as "cat /xc".
func TestShortenPathsReplacesOnlyWholePathPrefixes(t *testing.T) {
	sep := string(filepath.Separator)
	root := t.TempDir()
	home := filepath.Join(root, "me")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cwd := filepath.Join(root, "a", "b")
	inner := filepath.Join(cwd, "c")
	other := filepath.Join(root, "x") + cwd + sep + "c"
	got := ShortenPaths(cwd, "cp "+inner+" "+other+" --out="+inner)
	if want := "cp c " + other + " --out=c"; got != want {
		t.Fatalf("cwd: %q, want %q", got, want)
	}
	notHome := filepath.Join(root, "y") + home + sep + "z"
	got = ShortenPaths("", "ls '"+filepath.Join(home, "z")+"' "+notHome)
	if want := "ls '~" + sep + "z' " + notHome; got != want {
		t.Fatalf("home: %q, want %q", got, want)
	}
}
