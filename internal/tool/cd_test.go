package tool

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveDirAgainstTheSessionNotTheProcess(t *testing.T) {
	base := t.TempDir()
	here := filepath.Join(base, "here")
	sib := filepath.Join(base, "sibling")
	for _, d := range []string{here, sib, filepath.Join(here, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ arg, want string }{
		{"../sibling", sib},
		{"sub", filepath.Join(here, "sub")},
		{"./sub/../sub", filepath.Join(here, "sub")},
		{sib, sib},
		{"  " + sib + "  ", sib},
		{`"` + sib + `"`, sib},
	} {
		got, err := ResolveDir(here, c.arg)
		if err != nil {
			t.Fatalf("ResolveDir(%q): %v", c.arg, err)
		}
		if got != c.want {
			t.Errorf("ResolveDir(%q) = %q, want %q", c.arg, got, c.want)
		}
	}
}

func TestResolveDirExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got, err := ResolveDir(t.TempDir(), "~")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(home) {
		t.Fatalf("~ = %q, want %q", got, home)
	}
}

func TestResolveDirRefusals(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ arg, want string }{
		{"", "no directory given"},
		{"   ", "no directory given"},
		{"nope", "no such directory"},
		{"a.txt", "is not a directory"},
	} {
		_, err := ResolveDir(base, c.arg)
		if err == nil {
			t.Fatalf("ResolveDir(%q) should have failed", c.arg)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("ResolveDir(%q) = %v, want it to mention %q", c.arg, err, c.want)
		}
	}
}

func TestResolveDirRefusesUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root opens anything")
	}
	base := t.TempDir()
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := ResolveDir(base, "locked"); err == nil {
		t.Fatal("a directory that cannot be opened should be refused")
	}
}
