package snapshot

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitCfg sets a config key in the repository at root.
func gitCfg(t *testing.T, root, key, val string) {
	t.Helper()
	cmd := exec.Command("git", "config", key, val)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s: %v\n%s", key, err, out)
	}
}

// A restore gives back the bytes the snapshot was taken of, whatever the
// user's git does to line endings. Git for Windows ships
// core.autocrlf=true, which restored an LF file as CRLF; autocrlf=input
// stored a CRLF file as LF; an eol attribute converted on checkout.
func TestRestoreIsByteExactUnderLineEndingConversion(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(t *testing.T, root string)
	}{
		{"autocrlf=true", func(t *testing.T, root string) { gitCfg(t, root, "core.autocrlf", "true") }},
		{"autocrlf=input", func(t *testing.T, root string) { gitCfg(t, root, "core.autocrlf", "input") }},
		{"eol=crlf attribute", func(t *testing.T, root string) {
			write(t, filepath.Join(root, ".gitattributes"), "*.txt text eol=crlf\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initRepo(t)
			tc.prep(t, root)
			ctx := context.Background()
			r, err := Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			lf, crlf := filepath.Join(root, "a.txt"), filepath.Join(root, "win.txt")
			write(t, lf, "one\ntwo\n")
			write(t, crlf, "one\r\ntwo\r\n")
			s, err := r.Take(ctx, "turn 1")
			if err != nil {
				t.Fatal(err)
			}
			write(t, lf, "changed\n")
			write(t, crlf, "changed\n")
			if _, err := r.Restore(ctx, s); err != nil {
				t.Fatal(err)
			}
			if got := read(lf); got != "one\ntwo\n" {
				t.Errorf("LF file restored as %q, want %q", got, "one\ntwo\n")
			}
			if got := read(crlf); got != "one\r\ntwo\r\n" {
				t.Errorf("CRLF file restored as %q, want %q", got, "one\r\ntwo\r\n")
			}
		})
	}
}

// The shadow store ignores the user's global and system configuration,
// so a global autocrlf cannot touch a non-git directory's undo either.
func TestShadowIgnoresGlobalConfig(t *testing.T) {
	home := t.TempDir()
	write(t, filepath.Join(home, ".gitconfig"), "[core]\n\tautocrlf = true\n")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := t.TempDir()
	f := filepath.Join(dir, "notes.txt")
	write(t, f, "a\nb\n")
	ctx := context.Background()
	r, err := OpenShadow(ctx, dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Take(ctx, "turn 1")
	if err != nil {
		t.Fatal(err)
	}
	write(t, f, "x\n")
	if _, err := r.Restore(ctx, s); err != nil {
		t.Fatal(err)
	}
	if got := read(f); got != "a\nb\n" {
		t.Fatalf("restored %q, want %q", got, "a\nb\n")
	}
}
