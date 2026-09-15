package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestImpactHookExistingFileVsNewFile: the hook wired into every session
// (see buildAgent) reports the reach of an edit to a file that already
// exists, and says nothing for a file that does not — there is nothing
// yet to compare the write against, and a "reach" for code that does not
// exist would be a lie. No language server is on PATH in this test
// environment, so this also exercises the repomap fallback.
func TestImpactHookExistingFileVsNewFile(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root, "lib/parse.go", "package lib\n\nfunc Parse(s string) int {\n\treturn len(s)\n}\n\nfunc UseA() int { return Parse(\"a\") }\n")
	mustWrite(t, root, "lib/parse_test.go", "package lib\n\nimport \"testing\"\n\nfunc TestParse(t *testing.T) {\n\tif Parse(\"x\") != 1 {\n\t\tt.Fail()\n\t}\n}\n")
	mustWrite(t, root, "go.mod", "module example.com/x\n\ngo 1.22\n")

	ws := tool.NewLocal(root)
	diag := lsp.New(root, lsp.Config{Disabled: true})
	repo := &repomap.Cache{Root: root}
	hook := impactHook(agent.NewCD(root), ws, diag, repo, func() []string { return []string{"go test ./..."} })

	editInput, _ := json.Marshal(map[string]any{
		"path": "lib/parse.go", "old_string": "return len(s)", "new_string": "return len(s) + 1",
	})
	got := hook(context.Background(), agent.ToolCall{Name: "edit_file", Input: editInput})
	if got == "" {
		t.Fatal("expected a non-empty impact line for an edit to an existing, referenced function")
	}
	for _, want := range []string{"Parse", "checks: go test ./..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("impact line %q missing %q", got, want)
		}
	}

	writeInput, _ := json.Marshal(map[string]any{"path": "lib/new.go", "content": "package lib\n"})
	if got := hook(context.Background(), agent.ToolCall{Name: "write_file", Input: writeInput}); got != "" {
		t.Fatalf("a new file should have no impact, got %q", got)
	}

	noopInput, _ := json.Marshal(map[string]any{
		"path": "lib/parse.go", "old_string": "package lib", "new_string": "package lib",
	})
	if got := hook(context.Background(), agent.ToolCall{Name: "edit_file", Input: noopInput}); got != "" {
		t.Fatalf("a no-op edit should have no impact, got %q", got)
	}

	if got := hook(context.Background(), agent.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}); got != "" {
		t.Fatalf("a non-edit tool should have no impact, got %q", got)
	}
}

func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
