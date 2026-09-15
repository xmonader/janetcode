package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
