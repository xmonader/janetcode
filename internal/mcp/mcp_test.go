package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp/mcptest"
	"github.com/xmonader/janetcode/internal/trust"
)

// TestMain doubles as a fake MCP server when MCP_FAKE_SERVER is set, so the
// client is exercised over a real pipe and process boundary.
func TestMain(m *testing.M) {
	if os.Getenv("MCP_FAKE_SERVER") == "1" {
		// Stand in for the real shape of a stdio server: a wrapper that
		// forks the process doing the work. The child outlives its parent
		// unless the whole group is stopped.
		if pidfile := os.Getenv("MCP_FAKE_SERVER_CHILD"); pidfile != "" {
			child := exec.Command("sleep", "100")
			if err := child.Start(); err == nil {
				os.WriteFile(pidfile, []byte(strconv.Itoa(child.Process.Pid)), 0o644)
			}
		}
		mcptest.FakeServer()
		os.Exit(0)
	}
	if os.Getenv("MCP_FAKE_SERVER") == "env" {
		mcptest.EnvServer()
		os.Exit(0)
	}
	if os.Getenv("MCP_FAKE_SERVER") == "sticky" {
		mcptest.StickyServer()
		os.Exit(0)
	}
	if os.Getenv("MCP_FAKE_SERVER") == "reluctant" {
		mcptest.ReluctantServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func startFake(t *testing.T, env ...string) *Client {
	t.Helper()
	root := t.TempDir()
	c, err := Start(context.Background(), "fake srv", ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}}, Options{Root: root, Version: "test", HandshakeTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestHandshakeAndToolList(t *testing.T) {
	c := startFake(t)
	if c.ServerInfo.Name != "fake" || c.Protocol != "2024-11-05" {
		t.Fatalf("server info = %+v proto %s", c.ServerInfo, c.Protocol)
	}
	tools := c.Tools()
	if len(tools) != 4 || tools[0].Name != "echo" || tools[1].Name != "write thing!" {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestCallToolResults(t *testing.T) {
	c := startFake(t)
	if _, err := c.CallTool(context.Background(), "nope", nil); err == nil || !strings.Contains(err.Error(), "unknown tool nope") {
		t.Fatalf("rpc error = %v", err)
	}
	// Cancellation abandons a slow call promptly.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.CallTool(ctx, "slow", nil)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("slow call: err=%v after %s", err, time.Since(start))
	}
}

// TestStdioServerEnvIsScrubbed is the credential-leak fix: an MCP server
// is project-supplied, often fetched and run sight unseen, so it must not
// inherit the agent's API keys through the ambient environment. The probe
// server reports what it actually sees; the secrets set in this process
// must not cross the pipe, while an explicitly configured variable must.
func TestStdioServerEnvIsScrubbed(t *testing.T) {
	const marker = "sk-ant-envprobe-marker"
	t.Setenv("ANTHROPIC_API_KEY", marker)
	t.Setenv("OPENAI_API_KEY", "sk-oai-envprobe-marker")
	root := t.TempDir()
	c, err := Start(context.Background(), "env probe", ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"MCP_FAKE_SERVER": "env", "EXPLICIT_VAR": "explicit-ok"},
	}, Options{Root: root, Version: "test", HandshakeTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	probe := func(name string) string {
		res, err := c.CallTool(context.Background(), "env", json.RawMessage(`{"name":"`+name+`"}`))
		if err != nil {
			t.Fatalf("probe %s: %v", name, err)
		}
		return res.Text
	}
	if got := probe("ANTHROPIC_API_KEY"); strings.Contains(got, marker) {
		t.Fatalf("server saw the ambient ANTHROPIC_API_KEY: %q", got)
	}
	if got := probe("OPENAI_API_KEY"); strings.Contains(got, "sk-oai-envprobe-marker") {
		t.Fatalf("server saw the ambient OPENAI_API_KEY: %q", got)
	}
	if got := probe("EXPLICIT_VAR"); !strings.Contains(got, "explicit-ok") {
		t.Fatalf("server lost the explicitly configured env var: %q", got)
	}
}

// TestCallToolWriteRespectsContext is the wedged-server fix: a server
// that answered the handshake and then stopped reading fills its 64 KB
// pipe on the next sizable request, and a pipe write has no deadline —
// before the send took a context, this blocked the caller past
// cancellation (and, in the parallel read-only batch, the turn with it).
func TestCallToolWriteRespectsContext(t *testing.T) {
	root := t.TempDir()
	c, err := Start(context.Background(), "sticky", ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"MCP_FAKE_SERVER": "sticky"},
	}, Options{Root: root, Version: "test", HandshakeTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	big := strings.Repeat("x", 256<<10)
	args, err := json.Marshal(map[string]any{"text": big})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.CallTool(ctx, "echo", args)
	if err == nil {
		t.Fatal("call succeeded against a server that never reads")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the context's deadline", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("call took %s to abandon; the write ignored the context", elapsed)
	}
}

// TestAbandonedCallIsNotExecutedLater: bounding the write by returning on
// ctx.Done was not enough. The write stayed parked and completed when the
// server next drained stdin, so a tools/call the model had already been
// told had failed was delivered and run — at-most-once broken on the one
// operation where it matters. The write is now ended, not abandoned: the
// server either never sees the request or sees part of a frame, which
// cannot parse, and the connection is reported as unusable.
func TestAbandonedCallIsNotExecutedLater(t *testing.T) {
	root := t.TempDir()
	ran := t.TempDir()
	c, err := Start(context.Background(), "reluctant", ServerConfig{
		Command: os.Args[0],
		Env:     map[string]string{"MCP_FAKE_SERVER": "reluctant", "MCP_RAN_DIR": ran},
	}, Options{Root: root, Version: "test", HandshakeTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	args, err := json.Marshal(map[string]any{"text": strings.Repeat("x", 256<<10)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.CallTool(ctx, "ship", args); err == nil {
		t.Fatal("the call reported success against a server that was not reading")
	}
	// Long enough for the server to resume and drain everything the pipe
	// still holds.
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(filepath.Join(ran, "ran")); err == nil {
		t.Fatal("the cancelled call ran on the server after the caller was told it had failed")
	}
	// And the connection says it is finished rather than accepting a
	// request that would be read as the tail of the abandoned frame.
	if _, err := c.CallTool(context.Background(), "ship", []byte(`{"text":"small"}`)); err == nil {
		t.Fatal("a later call succeeded over a desynced connection")
	}
}

func TestServerExitFailsCalls(t *testing.T) {
	c := startFake(t)
	c.Close()
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit")
	}
	if _, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"x"}`)); err == nil {
		t.Fatal("call after exit must fail")
	}
}

func TestStartErrors(t *testing.T) {
	if _, err := Start(context.Background(), "x", ServerConfig{Command: ""}, Options{}); err == nil {
		t.Fatal("empty command accepted")
	}
	if _, err := Start(context.Background(), "x", ServerConfig{Command: "/nonexistent/binary"}, Options{}); err == nil {
		t.Fatal("missing binary accepted")
	}
	// A process that is not an MCP server: handshake must fail, not hang.
	_, err := Start(context.Background(), "cat", ServerConfig{Command: "sh", Args: []string{"-c", "echo not json; exit 1"}}, Options{HandshakeTimeout: 5 * time.Second})
	if err == nil {
		t.Fatal("non-server accepted")
	}
}

func TestToolName(t *testing.T) {
	if got := ToolName("my server", "do.thing"); got != "mcp__my_server__do_thing" {
		t.Fatalf("got %q", got)
	}
	long := ToolName("s", strings.Repeat("a", 100))
	if len(long) != 64 {
		t.Fatalf("len = %d", len(long))
	}
}

func TestProjectFileAndTrust(t *testing.T) {
	cwd := t.TempDir()
	if s, h, err := LoadProject(cwd); s != nil || h != "" || err != nil {
		t.Fatalf("missing file: %v %q %v", s, h, err)
	}
	os.WriteFile(cwd+"/.mcp.json", []byte(`{"mcpServers":{"a":{"command":"x","args":["1"]}}}`), 0o644)
	servers, hash, err := LoadProject(cwd)
	if err != nil || len(servers) != 1 || servers["a"].Command != "x" || hash == "" {
		t.Fatalf("servers = %+v hash %q err %v", servers, hash, err)
	}
	ts := trust.Store{Path: t.TempDir() + "/trust.json"}
	if ts.Trusted(cwd, hash) {
		t.Fatal("untrusted by default")
	}
	if err := ts.Trust(cwd, hash); err != nil {
		t.Fatal(err)
	}
	if !ts.Trusted(cwd, hash) || ts.Trusted(cwd, "other") || ts.Trusted("/elsewhere", hash) {
		t.Fatal("trust must match cwd and hash exactly")
	}
	os.WriteFile(cwd+"/.mcp.json", []byte(`{"mcpServers":{}}`), 0o644)
	_, hash2, _ := LoadProject(cwd)
	if ts.Trusted(cwd, hash2) {
		t.Fatal("edited file must lose trust")
	}
	os.WriteFile(cwd+"/.mcp.json", []byte(`{bad`), 0o644)
	if _, _, err := LoadProject(cwd); err == nil {
		t.Fatal("bad json accepted")
	}
}

func TestPrompts(t *testing.T) {
	c := startFake(t)
	if !c.Capabilities.Resources || !c.Capabilities.Prompts {
		t.Fatalf("capabilities = %+v", c.Capabilities)
	}
	ps := c.Prompts()
	if len(ps) != 1 || ps[0].Name != "review" || len(ps[0].Arguments) != 2 || !ps[0].Arguments[0].Required {
		t.Fatalf("prompts = %+v", ps)
	}
	text, err := c.GetPrompt(context.Background(), "review", map[string]string{"file": "a.go", "focus": "errors"})
	if err != nil || text != "Please review a.go focusing on errors\n<assistant>\nSure.\n</assistant>" {
		t.Fatalf("prompt = %q %v", text, err)
	}
}

// A server whose cursors cycle (page2 to page3 and back) is not followed
// round the loop until the handshake times out.
func TestToolListStopsOnACyclingCursor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Start(ctx, "fake srv", ServerConfig{Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1", "MCP_FAKE_CURSOR_CYCLE": "1"}}, Options{Root: t.TempDir(), Version: "test", HandshakeTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if tools := c.Tools(); len(tools) == 0 || tools[0].Name != "echo" {
		t.Fatalf("tools = %+v", tools)
	}
}
