package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomicCreatesWithPerm(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "file.txt")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q, want %q", data, "hello")
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestWriteFileAtomicOverwritesAndAppliesPerm(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even though the existing file was 0644, the helper always forces the
	// requested perm rather than preserving what's there.
	if err := WriteFileAtomic(p, []byte("new content"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "new content" {
		t.Fatalf("content = %q, want %q", data, "new content")
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestWriteFileAtomicNoTempFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file.txt")
	if err := WriteFileAtomic(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dir has %d entries, want 1 (no leftover temp file): %v", len(entries), entries)
	}
}

// AtomicWrite keeps its old default for the callers that write files a
// person reads with other tools; AtomicWriteMode is for the ones that
// write on the user's behalf.
func TestAtomicWriteModes(t *testing.T) {
	dir := t.TempDir()
	def := filepath.Join(dir, "public.json")
	if err := AtomicWrite(def, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(def); st.Mode().Perm() != DefaultFileMode {
		t.Errorf("AtomicWrite created %v, want %v", st.Mode().Perm(), DefaultFileMode)
	}
	priv := filepath.Join(dir, "private.json")
	if err := AtomicWriteMode(priv, []byte("{}"), PrivateFileMode); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(priv); st.Mode().Perm() != PrivateFileMode {
		t.Errorf("AtomicWriteMode created %v, want %v", st.Mode().Perm(), PrivateFileMode)
	}
}

// Neither form re-modes a file that already exists: a user who opened a
// file up, or locked it down, keeps their decision.
func TestAtomicWriteKeepsAnExistingMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "kept.json")
	if err := os.WriteFile(p, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteMode(p, []byte("new"), PrivateFileMode); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want the existing 0640", st.Mode().Perm())
	}
	data, _ := os.ReadFile(p)
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
}

// The temp file must not survive a write, whichever entry point wrote it:
// a sessions directory that fills up with .tmp-* leftovers is how the
// listing starts skipping files.
func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := AtomicWriteMode(filepath.Join(dir, "f.json"), []byte("x"), PrivateFileMode); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "f.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory holds %v, want only f.json", names)
	}
}

// A symlink is a pointer to the file, and writing the file must go
// through it. The rename used to land on the link itself, replacing it
// with a regular file: CLAUDE.md -> AGENTS.md became two diverging
// copies, and a dotfile managed by a symlink farm was silently detached
// from it, with the file the link named left as it was.
func TestAtomicWriteGoesThroughASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "AGENTS.md")
	link := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := AtomicWrite(link, []byte("new")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced by a %v", st.Mode())
	}
	if data, _ := os.ReadFile(target); string(data) != "new" {
		t.Fatalf("target = %q, want the new content", data)
	}
	if st, _ := os.Stat(target); st.Mode().Perm() != 0o640 {
		t.Fatalf("target mode = %v, want 0640 kept", st.Mode().Perm())
	}
	// A dangling link has nothing to write through; the write still
	// lands at the path it was given.
	dangling := filepath.Join(dir, "gone")
	if err := os.Symlink("nowhere", dangling); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(dangling, []byte("x")); err != nil {
		t.Fatal(err)
	}
}
