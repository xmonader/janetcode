package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemIncludesCwdAndInstructions(t *testing.T) {
	dir := t.TempDir()
	s := System(dir)
	if !strings.Contains(s, "Working directory: "+dir) || strings.Contains(s, "Project instructions") {
		t.Fatalf("prompt = %q", s)
	}
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("use tabs"), 0o644)
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("use spaces"), 0o644)
	s = System(dir)
	if !strings.Contains(s, "(from AGENTS.md)\nuse spaces") || strings.Contains(s, "use tabs") {
		t.Fatalf("precedence wrong: %q", s)
	}
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	if s = System(dir); !strings.Contains(s, "branch main") {
		t.Fatalf("git info missing: %q", s)
	}
}
