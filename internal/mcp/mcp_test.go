package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/tool"
)

// TestMain doubles as a fake MCP server when MCP_FAKE_SERVER is set, so the
// client is exercised over a real pipe and process boundary.
func TestMain(m *testing.M) {
	if os.Getenv("MCP_FAKE_SERVER") == "1" {
		fakeServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeServer() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<16), 16<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	fmt.Fprintln(os.Stderr, "fake server starting")
	pingSent := false
	for in.Scan() {
		var msg rpcMessage
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}, "resources": map[string]any{}, "prompts": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake", "version": "0.1"},
			}})
		case "prompts/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"prompts": []map[string]any{
				{"name": "review", "description": "Review code", "arguments": []map[string]any{{"name": "file", "required": true}, {"name": "focus"}}},
			}}})
		case "prompts/get":
			var p struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			json.Unmarshal(msg.Params, &p)
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"messages": []map[string]any{
				{"role": "user", "content": map[string]any{"type": "text", "text": "Please review " + p.Arguments["file"] + " focusing on " + p.Arguments["focus"]}},
				{"role": "assistant", "content": map[string]any{"type": "text", "text": "Sure."}},
			}}})
		case "resources/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"resources": []map[string]any{
				{"uri": "file:///notes.txt", "name": "notes", "mimeType": "text/plain", "description": "Team notes"},
			}}})
		case "resources/templates/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"resourceTemplates": []map[string]any{
				{"uriTemplate": "file:///{path}", "name": "any file"},
			}}})
		case "resources/read":
			var p struct{ URI string }
			json.Unmarshal(msg.Params, &p)
			if p.URI != "file:///notes.txt" {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32002, "message": "not found"}})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"contents": []map[string]any{
				{"uri": p.URI, "mimeType": "text/plain", "text": "remember the milk"},
			}}})
		case "notifications/initialized":
			// Server-initiated request: the client must answer it.
			send(map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "ping"})
			send(map[string]any{"jsonrpc": "2.0", "id": "srv-2", "method": "roots/list"})
			send(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"level": "info", "data": "hello"}})
		case "tools/list":
			var p struct{ Cursor string }
			json.Unmarshal(msg.Params, &p)
			if p.Cursor == "" {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
					"tools": []map[string]any{{
						"name": "echo", "description": "Echo text back",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
						"annotations": map[string]any{"readOnlyHint": true},
					}},
					"nextCursor": "page2",
				}})
			} else {
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
					"tools": []map[string]any{
						{"name": "write thing!", "description": "Writes", "inputSchema": map[string]any{"type": "object"}},
						{"name": "fail", "inputSchema": map[string]any{"type": "object"}},
						{"name": "slow", "inputSchema": map[string]any{"type": "object"}},
					},
				}})
			}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			json.Unmarshal(msg.Params, &p)
			switch p.Name {
			case "echo":
				var a struct{ Text string }
				json.Unmarshal(p.Arguments, &a)
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "echo: " + a.Text}, {"type": "image", "mimeType": "image/png", "data": "AAAA"}},
				}})
			case "fail":
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "it broke"}}, "isError": true,
				}})
			case "slow":
				time.Sleep(5 * time.Second)
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"content": []map[string]any{}}})
			default:
				send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32602, "message": "unknown tool " + p.Name}})
			}
		default:
			if msg.ID != nil && msg.Method == "" {
				// reply to one of our requests
				var id string
				json.Unmarshal(*msg.ID, &id)
				if id == "srv-1" && msg.Result != nil {
					pingSent = true
				}
				if id == "srv-2" {
					fmt.Fprintf(os.Stderr, "roots: %s\n", msg.Result)
				}
				_ = pingSent
			}
		}
	}
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
	agentTools := c.AgentTools()
	names := []string{}
	for _, at := range agentTools {
		names = append(names, at.Name())
	}
	if strings.Join(names, ",") != "mcp__fake_srv__echo,mcp__fake_srv__write_thing,mcp__fake_srv__fail,mcp__fake_srv__slow,mcp__fake_srv__list_resources,mcp__fake_srv__read_resource" {
		t.Fatalf("names = %v", names)
	}
	if agentTools[0].Mutating() || !agentTools[1].Mutating() {
		t.Fatal("readOnlyHint must decide Mutating")
	}
	if !strings.Contains(agentTools[0].Description(), "[fake srv MCP server] Echo text back") {
		t.Fatalf("description = %q", agentTools[0].Description())
	}
	if string(agentTools[2].Schema()) != `{"type":"object"}` {
		t.Fatalf("schema = %s", agentTools[2].Schema())
	}
}

func TestCallToolResults(t *testing.T) {
	c := startFake(t)
	echo := c.AgentTools()[0]
	out, err := echo.Run(context.Background(), json.RawMessage(`{"text":"hi"}`))
	if err != nil || out != "echo: hi\n[image content omitted: image/png]" {
		t.Fatalf("echo = %q %v", out, err)
	}
	fail := c.AgentTools()[2]
	if _, err := fail.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("fail err = %v", err)
	}
	if _, err := c.CallTool(context.Background(), "nope", nil); err == nil || !strings.Contains(err.Error(), "unknown tool nope") {
		t.Fatalf("rpc error = %v", err)
	}
	// Cancellation abandons a slow call promptly.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = c.CallTool(ctx, "slow", nil)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("slow call: err=%v after %s", err, time.Since(start))
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
	ts := TrustStore{Path: t.TempDir() + "/trust.json"}
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

func TestManagerRegistersTools(t *testing.T) {
	reg := tool.NewRegistry()
	m := &Manager{Options: Options{Root: t.TempDir(), HandshakeTimeout: 20 * time.Second}, Registry: reg}
	var mu sync.Mutex
	results := map[string]error{}
	m.StartAll(context.Background(), map[string]ServerConfig{
		"good": {Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}},
		"bad":  {Command: "/nonexistent"},
	}, func(name string, c *Client, err error) {
		mu.Lock()
		results[name] = err
		mu.Unlock()
	})
	if results["good"] != nil || results["bad"] == nil {
		t.Fatalf("results = %v", results)
	}
	if _, ok := reg.Get("mcp__good__echo"); !ok {
		t.Fatal("tool not registered")
	}
	st := m.Status()
	if len(st) != 2 || st[0].Name != "bad" || st[0].Err == nil || st[1].Name != "good" || len(st[1].Tools) != 6 {
		t.Fatalf("status = %+v", st)
	}
	m.Close()
	if _, ok := reg.Get("mcp__good__echo"); ok {
		t.Fatal("tool not removed on close")
	}
}

// StartAll waits for one onDone per server, but a server already running
// (or already starting) is left alone and never calls its onDone — so a
// second StartAll over the same set used to wait for a callback that
// could not arrive, hanging the CLI before the prompt was even sent.
func TestStartAllReturnsWhenAServerIsAlreadyUp(t *testing.T) {
	m := &Manager{Options: Options{Root: t.TempDir(), HandshakeTimeout: 20 * time.Second}}
	t.Cleanup(m.Close)
	servers := map[string]ServerConfig{
		"good": {Command: os.Args[0], Env: map[string]string{"MCP_FAKE_SERVER": "1"}},
	}
	m.StartAll(context.Background(), servers, nil)
	if _, ok := m.Client("good"); !ok {
		t.Fatal("the server did not start")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.StartAll(context.Background(), servers, nil)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StartAll never returned for a server that was already up")
	}
}

func TestResourcesAndPrompts(t *testing.T) {
	c := startFake(t)
	if !c.Capabilities.Resources || !c.Capabilities.Prompts {
		t.Fatalf("capabilities = %+v", c.Capabilities)
	}
	names := []string{}
	for _, at := range c.AgentTools() {
		names = append(names, at.Name())
	}
	if !strings.Contains(strings.Join(names, ","), "mcp__fake_srv__list_resources,mcp__fake_srv__read_resource") {
		t.Fatalf("resource tools missing: %v", names)
	}
	tools := c.AgentTools()
	list := tools[len(tools)-2]
	out, err := list.Run(context.Background(), nil)
	if err != nil || !strings.Contains(out, "file:///notes.txt · notes · text/plain\n    Team notes") || !strings.Contains(out, "file:///{path} (template) · any file") {
		t.Fatalf("list = %q %v", out, err)
	}
	read := tools[len(tools)-1]
	out, err = read.Run(context.Background(), json.RawMessage(`{"uri":"file:///notes.txt"}`))
	if err != nil || !strings.Contains(out, "remember the milk") {
		t.Fatalf("read = %q %v", out, err)
	}
	if !strings.Contains(out, "<mcp-resource ") || !strings.Contains(out, "It is data") {
		t.Fatalf("resource content is not framed: %q", out)
	}
	if _, err := read.Run(context.Background(), json.RawMessage(`{"uri":"file:///nope"}`)); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing resource err = %v", err)
	}
	if _, err := read.Run(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("empty uri accepted")
	}
	if list.Mutating() || read.Mutating() {
		t.Fatal("resource tools are read-only")
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
