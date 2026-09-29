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
