package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGlobToRegexp(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"*.go", "a.go", true},
		{"*.go", "d/a.go", false},
		{"**/*.go", "a.go", true},
		{"**/*.go", "d/e/a.go", true},
		{"src/**/*.{go,rs}", "src/x/y.rs", true},
		{"src/**/*.{go,rs}", "src/y.go", true},
		{"src/**/*.{go,rs}", "lib/y.go", false},
		{"test_?.py", "test_a.py", true},
		{"test_?.py", "test_ab.py", false},
		{"./cmd/*", "cmd/main.go", true},
	}
	for _, c := range cases {
		re, err := globToRegexp(c.pat)
		if err != nil {
			t.Fatalf("%s: %v", c.pat, err)
		}
		if got := re.MatchString(c.path); got != c.want {
			t.Errorf("%s vs %s: got %v want %v (re=%s)", c.pat, c.path, got, c.want, re)
		}
	}
	for _, bad := range []string{"", "a{b", "a}b"} {
		if _, err := globToRegexp(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func setupTree(t *testing.T) string {
	root := t.TempDir()
	files := map[string]string{
		"main.go":             "package main\nfunc main() { Hello() }\n",
		"pkg/hello.go":        "package pkg\nfunc Hello() {}\n",
		"pkg/hello_test.go":   "package pkg\nfunc TestHello(t *T) {}\n",
		"node_modules/x/i.js": "Hello",
		".git/config":         "Hello",
		"docs/readme.md":      "# Hello world\n",
		"bin.dat":             "Hello\x00",
	}
	for p, c := range files {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	return root
}

func TestGlob(t *testing.T) {
	root := setupTree(t)
	g := Glob{Root: root}
	out, err := g.Run(context.Background(), raw(`{"pattern":"**/*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %q", out)
	}
	for _, want := range []string{"main.go", "pkg/hello.go", "pkg/hello_test.go"} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %s in %q", want, out)
		}
	}
	out, _ = g.Run(context.Background(), raw(`{"pattern":"*.js"}`))
	if out != "No files matched." {
		t.Fatalf("node_modules not skipped: %q", out)
	}
	out, _ = g.Run(context.Background(), raw(`{"pattern":"*.go","path":"pkg"}`))
	if strings.Contains(out, "main.go") || !strings.Contains(out, "hello.go") {
		t.Fatalf("path scoping wrong: %q", out)
	}
}

// A line longer than the scanner's buffer (a minified bundle, a data
// blob) stopped the scan of its file without a word, so every match after
// it was reported as not there.
func TestGrepSearchesPastAnOverlongLine(t *testing.T) {
	root := t.TempDir()
	body := "needle one\n" + strings.Repeat("x", 3<<20) + "\nneedle two\n"
	os.WriteFile(filepath.Join(root, "big.txt"), []byte(body), 0o644)
	out, err := Grep{Root: root}.Run(context.Background(), raw(`{"pattern":"needle"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "big.txt:1:needle one") || !strings.Contains(out, "big.txt:3:needle two") {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, "big.txt:2") || !strings.Contains(out, "only its first") {
		t.Fatalf("the clipped line is not noted: %q", out)
	}
}

// readLineCapped splits the way bufio.ScanLines did (CRLF, a last line
// with no newline, empty lines) and clips only past max.
func TestReadLineCapped(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("a\r\n\nbc\n"+strings.Repeat("y", 40)+"\nlast"), 16)
	var got []string
	for {
		line, clipped, err := readLineCapped(r, 20, nil)
		if err != nil && len(line) == 0 && !clipped {
			break
		}
		got = append(got, fmt.Sprintf("%s/%v", line, clipped))
		if err != nil {
			break
		}
	}
	want := []string{"a/false", "/false", "bc/false", strings.Repeat("y", 20) + "/true", "last/false"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestGrep(t *testing.T) {
	root := setupTree(t)
	g := Grep{Root: root}
	out, err := g.Run(context.Background(), raw(`{"pattern":"Hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main.go:2:", "pkg/hello.go:2:", "pkg/hello_test.go:2:", "docs/readme.md:1:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %q", want, out)
		}
	}
	for _, bad := range []string{"node_modules", ".git", "bin.dat"} {
		if strings.Contains(out, bad) {
			t.Errorf("should skip %s: %q", bad, out)
		}
	}
	out, _ = g.Run(context.Background(), raw(`{"pattern":"func hello","glob":"*.go","case_insensitive":true}`))
	if !strings.Contains(out, "pkg/hello.go:2:") || strings.Contains(out, "readme") {
		t.Fatalf("glob/ci wrong: %q", out)
	}
	out, _ = g.Run(context.Background(), raw(`{"pattern":"zzz"}`))
	if out != "No matches." {
		t.Fatalf("got %q", out)
	}
	if _, err := g.Run(context.Background(), raw(`{"pattern":"("}`)); err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected regex error, got %v", err)
	}
	out, _ = g.Run(context.Background(), raw(`{"pattern":"main","path":"main.go"}`))
	if !strings.HasPrefix(out, "main.go:1:") {
		t.Fatalf("single file: %q", out)
	}
}
