package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What .aiignore names, the file tools neither show nor touch; the rest
// of the tree is unaffected.
func TestFileToolsHonourAIIgnore(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		".aiignore":         "secrets/\n*.key\n",
		"main.go":           "package main // needle\n",
		"secrets/token.txt": "needle\n",
		"sub/server.key":    "needle\n",
		"sub/ok.go":         "package sub // needle\n",
	} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	ctx := context.Background()
	run := func(tl Tool, in any) (string, error) {
		raw, _ := json.Marshal(in)
		return tl.Run(ctx, raw)
	}
	if _, err := run(ReadFile{Root: root}, map[string]any{"path": "secrets/token.txt"}); err == nil || !strings.Contains(err.Error(), ".aiignore:1") {
		t.Fatalf("read: %v", err)
	}
	if _, err := run(WriteFile{Root: root}, map[string]any{"path": "sub/new.key", "content": "x"}); err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sub/new.key")); err == nil {
		t.Fatal("excluded file written")
	}
	out, err := run(Grep{Root: root}, map[string]any{"pattern": "needle"})
	if err != nil || !strings.Contains(out, "main.go") || !strings.Contains(out, "sub/ok.go") || strings.Contains(out, "token") || strings.Contains(out, "server.key") {
		t.Fatalf("grep = %q %v", out, err)
	}
	out, err = run(Glob{Root: root}, map[string]any{"pattern": "**/*"})
	if err != nil || strings.Contains(out, "secrets") || strings.Contains(out, ".key") || !strings.Contains(out, "ok.go") {
		t.Fatalf("glob = %q %v", out, err)
	}
	out, err = run(ListDir{Root: root}, map[string]any{"path": "."})
	if err != nil || strings.Contains(out, "secrets") || !strings.Contains(out, "sub/") {
		t.Fatalf("list = %q %v", out, err)
	}
	if out, err = run(ReadFile{Root: root}, map[string]any{"path": "sub/ok.go"}); err != nil || !strings.Contains(out, "needle") {
		t.Fatalf("plain read = %q %v", out, err)
	}
}
