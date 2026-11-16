package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xmonader/janetcode/internal/shell"
)

func TestScriptUnavailableWithDockerSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sandbox")
	}
	python := pythonOrSkip(t)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	sb := &shell.Sandbox{}
	if err := sb.SetBackend(shell.BackendDocker); err != nil {
		t.Fatal(err)
	}
	ag := &Agent{}
	ag.Sandbox = sb
	s := &Script{Parent: ag, Python: python}
	if !s.Available() {
		t.Fatal("disabled sandbox should allow Python")
	}
	if err := sb.Enable(); err != nil {
		t.Fatal(err)
	}
	if s.Available() {
		t.Fatal("offered Python with an incompatible Docker sandbox")
	}
	sb.Disable()
	if !s.Available() {
		t.Fatal("disabling sandbox should restore Python")
	}
}
