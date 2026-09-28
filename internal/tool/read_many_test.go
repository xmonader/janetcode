package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file larger than anything a read could usefully show is refused on
// its size, before a byte of it is loaded. Both readers used to pull the
// whole file into memory — and decode, copy and split it — to show at
// most 2000 lines of it, so read_file on a multi-gigabyte log took the
// process down with it.
func TestReadersRefuseAHugeFileBeforeLoadingIt(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "huge.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Truncate(p, maxReadBytes+1); err != nil {
		t.Skipf("sparse files unavailable: %v", err)
	}
	_, err = (ReadFile{Root: root}).Run(context.Background(), raw(`{"path":"huge.log"}`))
	if err == nil || !strings.Contains(err.Error(), "too large") || !strings.Contains(err.Error(), "grep") {
		t.Fatalf("read_file on a huge file: %v", err)
	}
	out, err := (ReadManyFiles{Root: root}).Run(context.Background(), raw(`{"paths":["huge.log"]}`))
	if err != nil || !strings.Contains(out, "too large") {
		t.Fatalf("read_many_files on a huge file: %v %q", err, out)
	}
}

func TestReadManyFilesConcatenates(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.go"), []byte("package b\n"), 0o644)
	os.WriteFile(filepath.Join(root, "skip.bin"), []byte("ab\x00cd"), 0o644)
	r := ReadManyFiles{Root: root}
	out, err := r.Run(context.Background(), raw(`{"paths":["a.go","b.go","skip.bin"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "===== a.go =====") || !strings.Contains(out, "package a") {
		t.Fatalf("a.go missing: %q", out)
	}
	if !strings.Contains(out, "===== b.go =====") || !strings.Contains(out, "package b") {
		t.Fatalf("b.go missing: %q", out)
	}
	if !strings.Contains(out, "binary file") {
		t.Fatalf("binary not skipped: %q", out)
	}
}

func TestReadManyFilesGlob(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "one.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(root, "two.txt"), []byte("two\n"), 0o644)
	os.Mkdir(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "three.txt"), []byte("three\n"), 0o644)
	r := ReadManyFiles{Root: root}
	out, err := r.Run(context.Background(), raw(`{"glob":"**/*.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"one", "two", "three"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %q", want, out)
		}
	}
}

func TestReadManyFilesRejectsEscape(t *testing.T) {
	root := t.TempDir()
	r := ReadManyFiles{Root: root}
	out, err := r.Run(context.Background(), raw(`{"paths":["../etc/passwd"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "outside") {
		t.Fatalf("escape not reported: %q", out)
	}
}

func TestReadManyFilesNeedsInput(t *testing.T) {
	r := ReadManyFiles{Root: t.TempDir()}
	if _, err := r.Run(context.Background(), raw(`{}`)); err == nil {
		t.Fatal("empty input should fail")
	}
}
