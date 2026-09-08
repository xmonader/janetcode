package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFeedbackSyntaxAndCoveringTest(t *testing.T) {
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not installed")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "calc.go")
	os.WriteFile(good, []byte("package calc\n\nfunc Add(a, b int) int { return a + b }\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "calc_test.go"), []byte("package calc\n"), 0o644)
	if got := editFeedback(context.Background(), good); !strings.HasSuffix(strings.TrimSpace(got), "edited file): calc_test.go") || strings.Contains(got, "Syntax") {
		t.Fatalf("good file: %q", got)
	}
	bad := filepath.Join(dir, "broken.go")
	os.WriteFile(bad, []byte("package calc\n\nfunc Add(a, b int) int { return a + b \n"), 0o644)
	if got := editFeedback(context.Background(), bad); !strings.Contains(got, "Syntax check (gofmt) failed on broken.go") {
		t.Fatalf("bad file: %q", got)
	}
	if got := editFeedback(context.Background(), filepath.Join(dir, "calc_test.go")); got != "" {
		t.Fatalf("test file itself should get no pointer: %q", got)
	}
	if got := editFeedback(context.Background(), filepath.Join(dir, "notes.txt")); got != "" {
		t.Fatalf("unknown extension: %q", got)
	}
}
