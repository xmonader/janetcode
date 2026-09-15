package mcpcmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/mcp"
)

// repo writes a directory that ships an MCP server manifest.
func repo(t *testing.T, file, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFetchReadsADirectory(t *testing.T) {
	h := newHarness(t)
	src := repo(t, "mcp.json", `{"mcpServers": {"docs": {"command": "node", "args": ["server.js"]}, "remote": {"url": "https://example.test/mcp"}}}`)
	man, err := Fetch(context.Background(), h.env, src)
	if err != nil {
		t.Fatal(err)
	}
	if man.File != "mcp.json" || man.Cloned {
		t.Fatalf("manifest: %+v", man)
	}
	if strings.Join(man.Names, ",") != "docs,remote" {
		t.Fatalf("names: %v", man.Names)
	}
	// A command runs from the checkout; a URL has nothing to run from.
	if man.Servers["docs"].Cwd != src {
		t.Fatalf("cwd: %q want %q", man.Servers["docs"].Cwd, src)
	}
	if man.Servers["remote"].Cwd != "" {
		t.Fatalf("a remote server got a cwd: %+v", man.Servers["remote"])
	}
	if p := man.Preview(); !strings.Contains(p, "docs") || !strings.Contains(p, "node server.js") || !strings.Contains(p, "2 MCP servers") {
		t.Fatalf("preview:\n%s", p)
	}
	// Fetch writes nothing: the project file is only touched by Install.
	if _, err := os.Stat(filepath.Join(h.cwd, mcp.ProjectFile)); !os.IsNotExist(err) {
		t.Fatal("Fetch wrote the project file")
	}
}

func TestFetchErrors(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ name, file, body, want string }{
		{name: "no manifest", file: "README.md", body: "hello", want: "declares no MCP server"},
		{name: "not json", file: "mcp.json", body: "{", want: "mcp.json"},
		{name: "no servers", file: "mcp.json", body: `{"mcpServers": {}}`, want: "lists no servers"},
		{name: "bad name", file: "mcp.json", body: `{"mcpServers": {"two words": {"command": "x"}}}`, want: "spaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Fetch(context.Background(), h.env, repo(t, tc.file, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := Fetch(context.Background(), h.env, ""); err == nil {
		t.Fatal("an empty --from should fail")
	}
	// Without an install directory there is nowhere to clone to.
	if _, err := Fetch(context.Background(), h.env, "https://example.test/repo.git"); err == nil || !strings.Contains(err.Error(), "nowhere to clone") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallWritesEveryServer(t *testing.T) {
	h := newHarness(t)
	h.env.Manager = nil // this one is about the file, not about starting
	src := repo(t, mcp.ProjectFile, `{"mcpServers": {"a": {"command": "one"}, "b": {"url": "https://example.test/mcp"}}}`)
	man, err := Fetch(context.Background(), h.env, src)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Install(context.Background(), h.env, man)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "added 2 servers") {
		t.Fatalf("notes: %v", res.Notes)
	}
	servers, hash, err := mcp.LoadProject(h.cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers["a"].Cwd != src || servers["b"].URL == "" {
		t.Fatalf("servers: %+v", servers)
	}
	if !h.env.Trust.Trusted(h.cwd, hash) {
		t.Fatal("what --from wrote is not trusted")
	}
}

func TestFetchClonesARepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	ctx := context.Background()
	origin := repo(t, "mcp.json", `{"mcpServers": {"docs": {"command": "node", "args": ["server.js"]}}}`)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"add", "."},
		{"-c", "user.email=t@test", "-c", "user.name=t", "commit", "-qm", "manifest"},
	} {
		if _, err := gitx.Run(ctx, origin, []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"}, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	h := newHarness(t)
	h.env.InstallDir = filepath.Join(t.TempDir(), "servers")
	man, err := Fetch(ctx, h.env, "file://"+origin)
	if err != nil {
		t.Fatal(err)
	}
	if !man.Cloned || filepath.Dir(man.Dir) != h.env.InstallDir {
		t.Fatalf("manifest: %+v", man)
	}
	if man.Servers["docs"].Cwd != man.Dir {
		t.Fatalf("the cloned server runs from %q", man.Servers["docs"].Cwd)
	}
	// A second fetch updates the checkout instead of cloning again.
	again, err := Fetch(ctx, h.env, "file://"+origin)
	if err != nil {
		t.Fatal(err)
	}
	if again.Cloned || again.Dir != man.Dir {
		t.Fatalf("second fetch: %+v", again)
	}
}

func TestRepoName(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.test/o/docs-mcp.git": "docs-mcp",
		"git@example.test:o/docs-mcp.git":     "docs-mcp",
		"https://example.test/o/docs/":        "docs",
		"https://example.test/o/bad name.git": "",
	} {
		if got := RepoName(in); got != want {
			t.Fatalf("RepoName(%q) = %q want %q", in, got, want)
		}
	}
}
