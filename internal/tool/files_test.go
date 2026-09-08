package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"fmt"
	"strings"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestReadFileWindow(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644)
	r := ReadFile{Root: root}
	out, err := r.Run(context.Background(), raw(`{"path":"a.txt","offset":2,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "2\ttwo\n3\tthree\n... (1 more lines)\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if _, err := r.Run(context.Background(), raw(`{"path":"missing.txt"}`)); err == nil {
		t.Fatal("expected error for missing file")
	}
	if _, err := r.Run(context.Background(), raw(`{"path":"a.txt","bogus":1}`)); err == nil {
		t.Fatal("expected error for unknown field")
	}
	os.WriteFile(filepath.Join(root, "bin"), []byte("ab\x00cd"), 0o644)
	if _, err := r.Run(context.Background(), raw(`{"path":"bin"}`)); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary error, got %v", err)
	}
}

func TestPathEscapeRejected(t *testing.T) {
	root := t.TempDir()
	r := ReadFile{Root: root}
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "a/../../x"} {
		if _, err := r.Run(context.Background(), raw(`{"path":"`+p+`"}`)); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Errorf("%s: expected outside-root error, got %v", p, err)
		}
	}
	// Symlink pointing outside root.
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(root, "link"))
	if _, err := r.Run(context.Background(), raw(`{"path":"link/secret"}`)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("symlink: expected outside-root error, got %v", err)
	}
}

func TestWriteAndEdit(t *testing.T) {
	root := t.TempDir()
	w := WriteFile{Root: root}
	out, err := w.Run(context.Background(), raw(`{"path":"sub/dir/f.go","content":"package x\n\nfunc A() {}\nfunc B() {}\n"}`))
	if err != nil || out != "Created sub/dir/f.go (4 lines)" {
		t.Fatalf("write: %v %q", err, out)
	}
	e := EditFile{Root: root}
	if _, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func Z","new_string":"func Y"}`)); err == nil {
		t.Fatal("expected not-found error")
	}
	if _, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func ","new_string":"func "}`)); err == nil {
		t.Fatal("expected identical error")
	}
	if _, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func ","new_string":"fn "}`)); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	prev, err := e.Preview(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func A","new_string":"func Alpha"}`))
	if err != nil || !strings.Contains(prev, "-func A() {}\n+func Alpha() {}\n") {
		t.Fatalf("preview: %v\n%s", err, prev)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "sub/dir/f.go")); strings.Contains(string(got), "Alpha") {
		t.Fatal("preview must not modify the file")
	}
	if out, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func A","new_string":"func Alpha"}`)); err != nil || out != "Edited sub/dir/f.go (1 replacement, +1 -1 lines)" {
		t.Fatalf("edit: %v %q", err, out)
	}
	if _, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"() {}","new_string":"() { return }","replace_all":true}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "sub/dir/f.go"))
	want := "package x\n\nfunc Alpha() { return }\nfunc B() { return }\n"
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
	// Overwrite preserves mode.
	os.Chmod(filepath.Join(root, "sub/dir/f.go"), 0o755)
	if prev, err := w.Preview(context.Background(), raw(`{"path":"sub/dir/f.go","content":"x"}`)); err != nil || !strings.Contains(prev, "-package x\n") || !strings.Contains(prev, "+x\n") {
		t.Fatalf("write preview: %v\n%s", err, prev)
	}
	if prev, err := w.Preview(context.Background(), raw(`{"path":"brand/new.txt","content":"a\nb\n"}`)); err != nil || prev != "--- brand/new.txt\n+++ brand/new.txt\n@@ -1,0 +1,2 @@\n+a\n+b\n" {
		t.Fatalf("new file preview: %v\n%q", err, prev)
	}
	if out, err := w.Run(context.Background(), raw(`{"path":"sub/dir/f.go","content":"x"}`)); err != nil || out != "Overwrote sub/dir/f.go (+1 -4 lines)" {
		t.Fatalf("overwrite: %v %q", err, out)
	}
	st, _ := os.Stat(filepath.Join(root, "sub/dir/f.go"))
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("mode not preserved: %v", st.Mode())
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Join(root, "sub/dir"))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestListDir(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "d"), 0o755)
	os.WriteFile(filepath.Join(root, "b.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "a.txt"), nil, 0o644)
	l := ListDir{Root: root}
	out, err := l.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a.txt\nb.txt\nd/" {
		t.Fatalf("got %q", out)
	}
	if out, _ := l.Run(context.Background(), raw(`{"path":"d"}`)); out != "(empty directory)" {
		t.Fatalf("got %q", out)
	}
}

func TestRegistryDefsSortedAndDuplicatePanics(t *testing.T) {
	r := NewRegistry(WriteFile{}, ReadFile{}, ListDir{})
	defs := r.Defs()
	if len(defs) != 3 || defs[0].Name != "list_dir" || defs[1].Name != "read_file" || defs[2].Name != "write_file" {
		t.Fatalf("defs = %+v", defs)
	}
	for _, d := range defs {
		if !json.Valid(d.InputSchema) {
			t.Errorf("%s: invalid schema", d.Name)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate")
		}
	}()
	NewRegistry(ReadFile{}, ReadFile{})
}

func TestTruncateKeepsBothEnds(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "line %04d ok\n", i)
	}
	sb.WriteString("FAIL: TestSomething expected 1 got 2\n")
	out := TruncateTo(sb.String(), 4000)
	if len(out) > 4200 || !strings.HasPrefix(out, "line 0000 ok\n") || !strings.Contains(out, "FAIL: TestSomething") || !strings.Contains(out, "bytes omitted") {
		t.Fatalf("out = %q", out)
	}
	if got := TruncateTo("short", 100); got != "short" {
		t.Fatalf("got %q", got)
	}
}
