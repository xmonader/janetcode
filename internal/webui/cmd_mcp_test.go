package webui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/mcp/testserver"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
)

// TestFakeMCPServerProcess is not a test: TestWebMCPAddWritesAndStarts
// runs the test binary again with MCP_FAKE_SERVER=1, and this "test" then
// serves the protocol over stdin and stdout.
func TestFakeMCPServerProcess(t *testing.T) {
	if os.Getenv("MCP_FAKE_SERVER") != "1" {
		t.Skip("helper process only")
	}
	testserver.Run()
}

// mcpServer is a web server with a manager, an empty project and a trust
// store, working in its own directory.
func mcpServer(t *testing.T) *server {
	t.Helper()
	cwd := t.TempDir()
	mgr := &mcptool.Manager{Options: mcp.Options{Root: cwd, HandshakeTimeout: 20 * time.Second}, Registry: tool.NewRegistry()}
	t.Cleanup(mgr.Close)
	return newServer(context.Background(), Options{
		Cwd: cwd, ConfigDir: t.TempDir(),
		MCP: &MCPOptions{Manager: mgr, Trust: trust.Store{Path: filepath.Join(t.TempDir(), "trust.json")}},
	})
}

// text is everything the server has said so far.
func (s *server) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	for _, e := range s.entries {
		b.WriteString(e.Text + "\n" + e.Output + "\n")
	}
	return b.String()
}

// settle waits for a runBusy command to finish.
func (s *server) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("still running; said:\n%s", s.text())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWebMCPAddWritesAndStarts(t *testing.T) {
	s := mcpServer(t)
	// The fake server is this test binary re-executed.
	line := "/mcp add fake -e MCP_FAKE_SERVER=1 " + os.Args[0] + " -test.run=TestFakeMCPServerProcess"
	if err := s.runCommand(line); err != nil {
		t.Fatal(err)
	}
	s.settle(t)
	if out := s.text(); !strings.Contains(out, "added fake to "+mcp.ProjectFile) || !strings.Contains(out, "is up") {
		t.Fatalf("said:\n%s", out)
	}
	servers, hash, err := mcp.LoadProject(s.opts.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers["fake"].Env["MCP_FAKE_SERVER"] != "1" {
		t.Fatalf("servers: %+v", servers)
	}
	if !s.opts.MCP.Trust.Trusted(s.opts.Cwd, hash) || s.opts.MCP.ProjectHash != hash {
		t.Fatal("the file the command wrote is not trusted")
	}
	if st := s.opts.MCP.Manager.Status(); len(st) != 1 || len(st[0].Tools) == 0 {
		t.Fatalf("status: %+v", st)
	}
	// The listing shows it with its tools.
	if err := s.runCommand("/mcp"); err != nil {
		t.Fatal(err)
	}
	if out := s.text(); !strings.Contains(out, "✓ fake") || !strings.Contains(out, "echo") {
		t.Fatalf("listing:\n%s", out)
	}
}

func TestWebMCPAddErrors(t *testing.T) {
	s := mcpServer(t)
	for _, tc := range []struct{ line, want string }{
		{line: "/mcp add", want: "usage"},
		{line: "/mcp add docs", want: "needs the command"},
		{line: "/mcp add r https://x.test/mcp -e A=b", want: "--header"},
		{line: "/mcp wat", want: "usage"},
	} {
		if err := s.runCommand(tc.line); err != nil {
			t.Fatal(err)
		}
		if out := s.text(); !strings.Contains(out, tc.want) {
			t.Fatalf("%s: %q missing:\n%s", tc.line, tc.want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(s.opts.Cwd, mcp.ProjectFile)); !os.IsNotExist(err) {
		t.Fatal("a refused command wrote the project file")
	}
}

func TestWebMCPRemoveNeedsYes(t *testing.T) {
	s := mcpServer(t)
	if err := s.runCommand("/mcp add docs https://example.test/mcp"); err != nil {
		t.Fatal(err)
	}
	s.settle(t)
	if err := s.runCommand("/mcp remove docs"); err != nil {
		t.Fatal(err)
	}
	if out := s.text(); !strings.Contains(out, "--yes") {
		t.Fatalf("said:\n%s", out)
	}
	if servers, _, _ := mcp.LoadProject(s.opts.Cwd); len(servers) != 1 {
		t.Fatal("it was removed without --yes")
	}
	if err := s.runCommand("/mcp remove docs --yes"); err != nil {
		t.Fatal(err)
	}
	s.settle(t)
	if servers, _, _ := mcp.LoadProject(s.opts.Cwd); len(servers) != 0 {
		t.Fatalf("still there: %v", servers)
	}
	if err := s.runCommand("/mcp remove docs --yes"); err != nil {
		t.Fatal(err)
	}
	if out := s.text(); !strings.Contains(out, `no MCP server named "docs"`) {
		t.Fatalf("said:\n%s", out)
	}
}

func TestWebMCPAddFromShowsBeforeItWrites(t *testing.T) {
	s := mcpServer(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "mcp.json"), []byte(`{"mcpServers": {"docs": {"url": "https://example.test/mcp"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.runCommand("/mcp add --from " + src); err != nil {
		t.Fatal(err)
	}
	s.settle(t)
	if out := s.text(); !strings.Contains(out, "declares 1 MCP server") || !strings.Contains(out, "repeat with --yes") {
		t.Fatalf("said:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(s.opts.Cwd, mcp.ProjectFile)); !os.IsNotExist(err) {
		t.Fatal("the preview wrote the project file")
	}
	if err := s.runCommand("/mcp add --from " + src + " --yes"); err != nil {
		t.Fatal(err)
	}
	s.settle(t)
	if servers, _, _ := mcp.LoadProject(s.opts.Cwd); len(servers) != 1 || servers["docs"].URL == "" {
		t.Fatalf("servers: %+v", servers)
	}
}

func TestWebMCPWithoutAManager(t *testing.T) {
	s := newServer(context.Background(), Options{Cwd: t.TempDir()})
	if err := s.runCommand("/mcp"); err != nil {
		t.Fatal(err)
	}
	if out := s.text(); !strings.Contains(out, "not configured") {
		t.Fatalf("said:\n%s", out)
	}
}
