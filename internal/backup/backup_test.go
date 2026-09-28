package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A backup taken on one machine restores on another: every file comes
// back under the other machine's directories, with its mode, and the
// archive itself is private.
func TestBackupThenRestoreElsewhere(t *testing.T) {
	src := Roots{"config": filepath.Join(t.TempDir(), "cfg"), "data": filepath.Join(t.TempDir(), "data")}
	put(t, filepath.Join(src["config"], "config.json"), `{"model":"m"}`, 0o600)
	put(t, filepath.Join(src["config"], "permissions", "rules.json"), `[]`, 0o644)
	put(t, filepath.Join(src["data"], "sessions", "abc.jsonl"), "{}\n{}\n", 0o600)
	if err := os.Symlink("/etc/passwd", filepath.Join(src["data"], "link")); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "jc.tar.gz")
	sum, err := Create(archive, src)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 3 {
		t.Fatalf("backed up %d files, want 3 (the symlink is not state)", sum.Files)
	}
	if fi, _ := os.Stat(archive); fi.Mode().Perm() != 0o600 {
		t.Errorf("archive mode %04o; it holds API keys", fi.Mode().Perm())
	}

	dst := Roots{"config": filepath.Join(t.TempDir(), "cfg"), "data": filepath.Join(t.TempDir(), "data")}
	got, err := Restore(archive, dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.Files != 3 || got.Before != "" {
		t.Fatalf("restore = %+v", got)
	}
	if read(t, filepath.Join(dst["data"], "sessions", "abc.jsonl")) != "{}\n{}\n" {
		t.Error("session not restored")
	}
	if fi, _ := os.Stat(filepath.Join(dst["config"], "config.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %04o after restore", fi.Mode().Perm())
	}
}

// Restoring over existing state saves what it replaces first, so the
// restore itself can be undone, and leaves files the archive lacks alone.
func TestRestoreSavesWhatItReplaces(t *testing.T) {
	roots := Roots{"config": filepath.Join(t.TempDir(), "cfg"), "data": filepath.Join(t.TempDir(), "data")}
	put(t, filepath.Join(roots["config"], "config.json"), `{"model":"old"}`, 0o600)
	archive := filepath.Join(t.TempDir(), "jc.tar.gz")
	if _, err := Create(archive, roots); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(roots["config"], "config.json"), `{"model":"new"}`, 0o600)
	put(t, filepath.Join(roots["data"], "memory", "note.md"), "keep me", 0o600)

	sum, err := Restore(archive, roots)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(roots["config"], "config.json")) != `{"model":"old"}` {
		t.Error("config not restored")
	}
	if read(t, filepath.Join(roots["data"], "memory", "note.md")) != "keep me" {
		t.Error("a file the archive does not hold was touched")
	}
	if sum.Before == "" {
		t.Fatal("the replaced config was not saved")
	}
	if _, err := Restore(sum.Before, roots); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(roots["config"], "config.json")) != `{"model":"new"}` {
		t.Error("undoing the restore did not bring the replaced config back")
	}
}

// A hostile archive is refused whole, before anything is written.
func TestRestoreRefusesEscapesAndLinks(t *testing.T) {
	for _, tc := range []struct {
		name string
		hdr  tar.Header
	}{
		{"parent path", tar.Header{Name: "config/../../evil", Typeflag: tar.TypeReg, Size: 1, Mode: 0o600}},
		{"absolute", tar.Header{Name: "/etc/evil", Typeflag: tar.TypeReg, Size: 1, Mode: 0o600}},
		{"backslash", tar.Header{Name: `config/..\..\evil`, Typeflag: tar.TypeReg, Size: 1, Mode: 0o600}},
		{"unknown root", tar.Header{Name: "home/.bashrc", Typeflag: tar.TypeReg, Size: 1, Mode: 0o600}},
		{"symlink", tar.Header{Name: "config/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			f, _ := os.Create(archive)
			gz := gzip.NewWriter(f)
			tw := tar.NewWriter(gz)
			good := tar.Header{Name: "config/config.json", Typeflag: tar.TypeReg, Size: 2, Mode: 0o600}
			tw.WriteHeader(&good)
			tw.Write([]byte("{}"))
			tw.WriteHeader(&tc.hdr)
			if tc.hdr.Size > 0 {
				tw.Write([]byte("x"))
			}
			tw.Close()
			gz.Close()
			f.Close()

			roots := Roots{"config": filepath.Join(t.TempDir(), "cfg"), "data": filepath.Join(t.TempDir(), "data")}
			_, err := Restore(archive, roots)
			if err == nil || !strings.Contains(err.Error(), "refusing the archive") {
				t.Fatalf("err = %v", err)
			}
			if _, err := os.Stat(filepath.Join(roots["config"], "config.json")); err == nil {
				t.Error("the good entry before the bad one was written")
			}
		})
	}
}

// A backup written into one of the directories it backs up does not
// contain itself.
// With nothing to back up, an existing file at dest is left alone: on a
// fresh machine "--backup old.tar.gz" typed for "--restore" replaced the
// user's backup with an empty archive, which the CLI then deleted.
func TestBackupOfNothingLeavesDestAlone(t *testing.T) {
	roots := Roots{"config": filepath.Join(t.TempDir(), "cfg"), "data": filepath.Join(t.TempDir(), "data")}
	dest := filepath.Join(t.TempDir(), "old.tar.gz")
	put(t, dest, "the user's earlier backup", 0o600)
	sum, err := Create(dest, roots)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 0 {
		t.Fatalf("backed up %d files from empty roots", sum.Files)
	}
	if got := read(t, dest); got != "the user's earlier backup" {
		t.Fatalf("dest = %q, want it untouched", got)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".janetcode-backup-*")); len(left) != 0 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestBackupInsideARoot(t *testing.T) {
	roots := Roots{"config": filepath.Join(t.TempDir(), "cfg"), "data": filepath.Join(t.TempDir(), "data")}
	put(t, filepath.Join(roots["data"], "sessions", "a.jsonl"), strings.Repeat("x", 1<<16), 0o600)
	sum, err := Create(filepath.Join(roots["data"], "jc.tar.gz"), roots)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 1 {
		t.Fatalf("backed up %d files, want 1", sum.Files)
	}
	// The same with dest named relative to a working directory inside the
	// root: the walk sees absolute paths, and a relative temp name never
	// matched one, so the archive swallowed its own half-written self.
	t.Chdir(roots["data"])
	sum, err = Create("jc2.tar.gz", roots)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Files != 2 { // a.jsonl and the first backup, not the second
		t.Fatalf("backed up %d files, want 2", sum.Files)
	}
}
