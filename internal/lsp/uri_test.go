package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A path with a space or a non-ASCII name goes out escaped, round-trips,
// and a server's differently escaped spelling of it lands on the same key.
func TestURIEscapesAndRoundTrips(t *testing.T) {
	for _, p := range []string{"/tmp/my project/a.go", "/tmp/naïve/#1?.go", "/tmp/100%/a.go"} {
		abs := filepath.FromSlash(p)
		u := toURI(abs)
		if strings.ContainsAny(u[len("file://"):], " #?") {
			t.Fatalf("toURI(%q) = %q is not escaped", p, u)
		}
		if got := fromURI(u); got != abs {
			t.Fatalf("fromURI(toURI(%q)) = %q", p, got)
		}
	}
	if got, want := toURI(filepath.FromSlash("/tmp/my project/a.go")), "file:///tmp/my%20project/a.go"; got != want {
		t.Fatalf("toURI = %q, want %q", got, want)
	}
	// A server that escapes more than we do (vscode-uri escapes "@").
	if got, want := canonicalURI("file:///tmp/%40scope/a.go"), toURI(filepath.FromSlash("/tmp/@scope/a.go")); got != want {
		t.Fatalf("canonicalURI = %q, want %q", got, want)
	}
}

// A symbol found after non-ASCII text on its line is sent at its UTF-16
// column, which is what the protocol counts in; the byte offset pointed
// past it, at nothing.
func TestGoplsSymbolAfterNonASCII(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nimport \"strconv\"\n\nfunc G() (string, int) { return \"日本語日本語日本語\", strconv.IntSize }\n"), 0o644)
	m := New(root, Config{Timeout: 20})
	defer m.Close()
	hov, err := m.Hover(context.Background(), "m.go", Point{Symbol: "IntSize"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hov, "IntSize") {
		t.Fatalf("hover = %q", hov)
	}
}

func TestUTF16Col(t *testing.T) {
	for _, tc := range []struct {
		line string
		b    int
		want int
	}{
		{"abc", 2, 2},
		{"é x", 3, 2},    // é is two bytes, one unit
		{"日本 x", 7, 3},   // three bytes each, one unit each
		{"😀 x", 5, 3},    // four bytes, a surrogate pair
		{"ab", 5, 2},     // past the end clamps
		{"a\xffb", 2, 2}, // an invalid byte counts as one unit
	} {
		if got := utf16Col(tc.line, tc.b); got != tc.want {
			t.Errorf("utf16Col(%q, %d) = %d, want %d", tc.line, tc.b, got, tc.want)
		}
	}
}

// A real gopls publishes its diagnostics under the escaped URI; with the
// unescaped one we sent, the wait never matched them and timed out.
func TestGoplsPathWithSpace(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	root := filepath.Join(t.TempDir(), "my project")
	os.MkdirAll(root, 0o755)
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nfunc F() int { return \"no\" }\n"), 0o644)
	m := New(root, Config{Timeout: 20})
	defer m.Close()
	lines, ok, err := m.Diagnose(context.Background(), "m.go")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v lines=%q known=%q", ok, err, lines, m.Known())
	}
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "m.go:3:") {
		t.Fatalf("lines = %q", lines)
	}
}
