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

// A symlink to an excluded file is the excluded file: the rules were
// matched against the name the model gave, so a link checked into the
// repo (or one bash made) under an unexcluded name read and overwrote
// what .aiignore keeps away.
func TestAIIgnoreFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, ".aiignore"), []byte("secrets/\n.env\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "secrets"), 0o755)
	os.WriteFile(filepath.Join(root, "secrets", "token.txt"), []byte("needle\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("KEY=needle\n"), 0o644)
	if err := os.Symlink(filepath.Join(root, "secrets"), filepath.Join(root, "notsecret")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	os.Symlink(".env", filepath.Join(root, "config.txt"))
	ctx := context.Background()
	for _, p := range []string{"notsecret/token.txt", "config.txt"} {
		raw, _ := json.Marshal(map[string]any{"path": p})
		if out, err := (ReadFile{Root: root}).Run(ctx, raw); err == nil || !strings.Contains(err.Error(), "excluded") {
			t.Errorf("read %s = %q %v, want it excluded", p, out, err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"path": "config.txt", "content": "x"})
	if _, err := (WriteFile{Root: root}).Run(ctx, raw); err == nil {
		t.Error("wrote through a link to an excluded file")
	}
	if b, _ := os.ReadFile(filepath.Join(root, ".env")); string(b) != "KEY=needle\n" {
		t.Errorf(".env = %q", b)
	}
}
