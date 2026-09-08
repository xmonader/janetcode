package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGoplsRealDiagnostics runs the real server when it is installed.
func TestGoplsRealDiagnostics(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.22\n"), 0o644)
	os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nfunc F() int { return \"no\" }\n"), 0o644)
	m := New(root, Config{Timeout: 60})
	defer m.Close()
	lines, ok, err := m.Diagnose(context.Background(), "m.go")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v lines=%q", ok, err, lines)
	}
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "m.go:3:") || !strings.Contains(lines[0], "error:") {
		t.Fatalf("lines = %q", lines)
	}
	os.WriteFile(filepath.Join(root, "m.go"), []byte("package m\n\nfunc F() int { return 1 }\n"), 0o644)
	if lines, ok, err := m.Diagnose(context.Background(), "m.go"); err != nil || !ok || len(lines) != 0 {
		t.Fatalf("after fix: ok=%v err=%v lines=%q", ok, err, lines)
	}
}
