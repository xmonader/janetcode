package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readProject(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ProjectFile))
	if err != nil {
		t.Fatalf("read %s: %v", ProjectFile, err)
	}
	return string(b)
}

func TestAddProjectServerCreatesFile(t *testing.T) {
	dir := t.TempDir()
	servers, hash, err := AddProjectServer(dir, "docs", ServerConfig{Command: "npx", Args: []string{"-y", "docs-mcp"}, Env: map[string]string{"TOKEN": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if hash == "" {
		t.Fatal("no content hash")
	}
	if got := servers["docs"].Command; got != "npx" {
		t.Fatalf("command %q", got)
	}
	text := readProject(t, dir)
	if !strings.Contains(text, `"mcpServers"`) || !strings.Contains(text, `"docs-mcp"`) {
		t.Fatalf("file does not hold the server:\n%s", text)
	}
	// It has to read back through the ordinary loader, hash and all.
	back, backHash, err := LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if backHash != hash || len(back) != 1 || back["docs"].Env["TOKEN"] != "x" {
		t.Fatalf("round trip: %+v hash=%q want %q", back, backHash, hash)
	}
}

func TestAddProjectServerHTTPShape(t *testing.T) {
	dir := t.TempDir()
	servers, _, err := AddProjectServer(dir, "remote", ServerConfig{URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "Bearer t"}})
	if err != nil {
		t.Fatal(err)
	}
	got := servers["remote"]
	if got.URL != "https://example.test/mcp" || got.Headers["Authorization"] != "Bearer t" {
		t.Fatalf("http server round trip: %+v", got)
	}
	if strings.Contains(readProject(t, dir), `"command"`) {
		t.Fatal("a URL server should not write an empty command")
	}
}

func TestAddProjectServerPreservesTheRestOfTheFile(t *testing.T) {
	dir := t.TempDir()
	original := `{
    "$schema": "https://example.test/schema.json",
    "mcpServers": {
        "first": {
            "command": "one"
        },
        "second": {
            "command": "two",
            "args": ["--flag"]
        }
    },
    "other": {"kept": true}
}
`
	if err := os.WriteFile(filepath.Join(dir, ProjectFile), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	servers, _, err := AddProjectServer(dir, "third", ServerConfig{Command: "three"})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 3 {
		t.Fatalf("servers: %v", servers)
	}
	text := readProject(t, dir)
	for _, want := range []string{`"$schema"`, `"other"`, `"kept": true`, `"--flag"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost %s:\n%s", want, text)
		}
	}
	// Order is kept, the new entry last, and so is the file's own indent.
	if i, j, k := strings.Index(text, `"first"`), strings.Index(text, `"second"`), strings.Index(text, `"third"`); !(i < j && j < k) {
		t.Fatalf("order shuffled:\n%s", text)
	}
	if !strings.Contains(text, "\n    \"$schema\"") {
		t.Fatalf("four-space indent not preserved:\n%s", text)
	}
}

func TestAddProjectServerReplacesSameName(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := AddProjectServer(dir, "one", ServerConfig{Command: "old"}); err != nil {
		t.Fatal(err)
	}
	servers, _, err := AddProjectServer(dir, "one", ServerConfig{Command: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers["one"].Command != "new" {
		t.Fatalf("servers: %+v", servers)
	}
}

func TestRemoveProjectServer(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := AddProjectServer(dir, "one", ServerConfig{Command: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddProjectServer(dir, "two", ServerConfig{Command: "b"}); err != nil {
		t.Fatal(err)
	}
	servers, hash, err := RemoveProjectServer(dir, "one")
	if err != nil {
		t.Fatal(err)
	}
	if _, still := servers["one"]; still || len(servers) != 1 {
		t.Fatalf("servers: %+v", servers)
	}
	if _, backHash, _ := LoadProject(dir); backHash != hash {
		t.Fatal("hash does not match the file on disk")
	}
	if _, _, err := RemoveProjectServer(dir, "nope"); err == nil {
		t.Fatal("removing an unknown server should fail")
	}
}

func TestEditProjectRejectsBadJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProjectFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddProjectServer(dir, "one", ServerConfig{Command: "a"}); err == nil {
		t.Fatal("expected a parse error")
	}
	if got := readProject(t, dir); got != "{not json" {
		t.Fatalf("the unreadable file was rewritten: %q", got)
	}
}

func TestAddProjectServerEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProjectFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	servers, _, err := AddProjectServer(dir, "one", ServerConfig{Command: "a"})
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers=%v err=%v", servers, err)
	}
}

func TestValidServerName(t *testing.T) {
	for _, bad := range []string{"", "   ", "two words", "a/b", `q"uote`} {
		if err := ValidServerName(bad); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
	}
	for _, ok := range []string{"docs", "my-server", "server_2"} {
		if err := ValidServerName(ok); err != nil {
			t.Fatalf("%q: %v", ok, err)
		}
	}
}
