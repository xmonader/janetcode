package mcpcmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptest"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
)

// TestMain doubles as the fake MCP server when MCP_FAKE_SERVER is set, as
// internal/mcp's own tests do, so a server is really started over a pipe.
func TestMain(m *testing.M) {
	if os.Getenv("MCP_FAKE_SERVER") == "1" {
		mcptest.FakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type harness struct {
	env      Env
	cwd      string
	mgr      *mcptool.Manager
	reg      *tool.Registry
	updates  int
	servers  map[string]mcp.ServerConfig
	lastHash string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cwd := t.TempDir()
	h := &harness{cwd: cwd, reg: tool.NewRegistry()}
	h.mgr = &mcptool.Manager{Options: mcp.Options{Root: cwd, Version: "test", HandshakeTimeout: 20 * time.Second}, Registry: h.reg}
	t.Cleanup(h.mgr.Close)
	h.env = Env{
		Cwd: cwd, Manager: h.mgr, Wait: 20 * time.Second,
		Trust: trust.Store{Path: filepath.Join(t.TempDir(), "mcp-trust.json")},
		Updated: func(servers map[string]mcp.ServerConfig, hash string) {
			h.updates++
			h.servers, h.lastHash = servers, hash
		},
	}
	return h
}

// fake is a server config that re-executes the test binary as an MCP
// server on stdio.
func fake() mcp.ServerConfig {
	return mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}}
}

func TestAddStartsTheServerAndTrustsTheFile(t *testing.T) {
	h := newHarness(t)
	res, err := Add(context.Background(), h.env, "fake", fake())
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("start failed: %s", res.Error)
	}
	notes := strings.Join(res.Notes, "\n")
	if !strings.Contains(notes, "added fake to "+mcp.ProjectFile) || !strings.Contains(notes, "is up") {
		t.Fatalf("notes: %v", res.Notes)
	}
	if !strings.Contains(res.Sheet, "echo") {
		t.Fatalf("tool sheet: %q", res.Sheet)
	}
	// The written file is trusted, so the next session starts it without
	// asking, and the front end heard about it.
	_, hash, err := mcp.LoadProject(h.cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !h.env.Trust.Trusted(h.cwd, hash) {
		t.Fatal("the file the user just wrote is not trusted")
	}
	if h.updates != 1 || h.lastHash != hash || len(h.servers) != 1 {
		t.Fatalf("updates=%d hash=%q servers=%v", h.updates, h.lastHash, h.servers)
	}
	// The server is running and its tools reached the registry.
	if st := h.mgr.Status(); len(st) != 1 || st[0].Name != "fake" || len(st[0].Tools) == 0 {
		t.Fatalf("status: %+v", st)
	}
	if len(h.reg.Defs()) == 0 {
		t.Fatal("no tools registered")
	}
}

func TestAddReportsAServerThatDoesNotStart(t *testing.T) {
	h := newHarness(t)
	res, err := Add(context.Background(), h.env, "broken", mcp.ServerConfig{Command: filepath.Join(h.cwd, "nope")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" || !strings.Contains(res.Error, "did not start") {
		t.Fatalf("result: %+v", res)
	}
	// It is still written, so the entry can be corrected rather than retyped.
	if servers, _, _ := mcp.LoadProject(h.cwd); len(servers) != 1 {
		t.Fatalf("servers: %v", servers)
	}
}

func TestAddRejectsAnEmptyServer(t *testing.T) {
	h := newHarness(t)
	if _, err := Add(context.Background(), h.env, "empty", mcp.ServerConfig{}); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := Add(context.Background(), h.env, "two words", fake()); err == nil {
		t.Fatal("expected a name error")
	}
	if _, err := os.Stat(filepath.Join(h.cwd, mcp.ProjectFile)); !os.IsNotExist(err) {
		t.Fatal("a refused add should not create the file")
	}
}

func TestRemoveStopsTheServer(t *testing.T) {
	h := newHarness(t)
	if _, err := Add(context.Background(), h.env, "fake", fake()); err != nil {
		t.Fatal(err)
	}
	res, err := Remove(context.Background(), h.env, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "removed fake") {
		t.Fatalf("notes: %v", res.Notes)
	}
	if st := h.mgr.Status(); len(st) != 0 {
		t.Fatalf("still running: %+v", st)
	}
	if len(h.reg.Defs()) != 0 {
		t.Fatalf("tools left behind: %d", len(h.reg.Defs()))
	}
	if servers, hash, _ := mcp.LoadProject(h.cwd); len(servers) != 0 || !h.env.Trust.Trusted(h.cwd, hash) {
		t.Fatalf("servers=%v trusted=%v", servers, h.env.Trust.Trusted(h.cwd, hash))
	}
	if _, err := Remove(context.Background(), h.env, "fake"); err == nil {
		t.Fatal("removing it twice should fail")
	}
}

func TestAddNotesAShadowedName(t *testing.T) {
	h := newHarness(t)
	h.env.Global = map[string]mcp.ServerConfig{"fake": {Command: "elsewhere"}}
	res, err := Add(context.Background(), h.env, "fake", fake())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Tips, "\n"), "config file") {
		t.Fatalf("tips: %v", res.Tips)
	}
}
