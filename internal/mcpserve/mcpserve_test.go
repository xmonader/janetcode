package mcpserve_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcpserve"
)

type fakeRunner struct {
	mu    sync.Mutex
	tasks []mcpserve.Task
	block chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, t mcpserve.Task, progress func(string)) (mcpserve.Report, error) {
	f.mu.Lock()
	f.tasks = append(f.tasks, t)
	block := f.block
	f.mu.Unlock()
	if progress != nil {
		progress("read_file main.go")
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return mcpserve.Report{SessionID: "s1", StopReason: "cancelled"}, nil
		}
	}
	id := t.SessionID
	if id == "" {
		id = "s1"
	}
	return mcpserve.Report{SessionID: id, Text: "did: " + t.Prompt, StopReason: "end_turn", Verdict: "✓ tested", ToolCalls: 2, Denied: 1, InputTokens: 10, OutputTokens: 5}, nil
}

// TestMain lets the test binary serve MCP itself, so the repository's own
// MCP client can talk to it over a real pipe and process boundary.
func TestMain(m *testing.M) {
	if os.Getenv("MCPSERVE_TEST_SERVER") == "1" {
		_ = mcpserve.Serve(context.Background(), os.Stdin, os.Stdout, mcpserve.Options{Runner: &fakeRunner{}, Version: "test", Mode: "read-only", ReadOnly: true, Dir: "/work"})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// janetcode's own MCP client can list and call the tool.
func TestTheRepositorysOwnClientCallsTheTool(t *testing.T) {
	c, err := mcp.Start(context.Background(), "jc", mcp.ServerConfig{Command: os.Args[0], Env: map[string]string{"MCPSERVE_TEST_SERVER": "1"}}, mcp.Options{Root: t.TempDir(), HandshakeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tools := c.Tools()
	if len(tools) != 1 || tools[0].Name != mcpserve.ToolName {
		t.Fatalf("tools = %+v", tools)
	}
	if ro := tools[0].Annotations.ReadOnlyHint; ro == nil || !*ro {
		t.Fatal("a read-only server's tool is not annotated read-only")
	}
	if !strings.Contains(tools[0].Description, "/work") || !strings.Contains(tools[0].Description, "read-only") {
		t.Fatalf("description = %q", tools[0].Description)
	}
	res, err := c.CallTool(context.Background(), mcpserve.ToolName, json.RawMessage(`{"prompt":"fix the bug"}`))
	if err != nil || res.IsError {
		t.Fatalf("call = %+v, %v", res, err)
	}
	for _, want := range []string{"did: fix the bug", "session s1", "end_turn", "2 tool calls", "1 refused by the permission mode", "✓ tested", "10 in / 5 out tokens"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("result text %q lacks %q", res.Text, want)
		}
	}
	res, err = c.CallTool(context.Background(), mcpserve.ToolName, json.RawMessage(`{"prompt":""}`))
	if err != nil || !res.IsError {
		t.Fatalf("empty prompt = %+v, %v", res, err)
	}
}

// Raw protocol: structured content, progress and cancellation.
func TestStructuredProgressAndCancel(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &fakeRunner{}
	go func() {
		_ = mcpserve.Serve(context.Background(), inR, outW, mcpserve.Options{Runner: r, Version: "t", Dir: "/w"})
		outW.Close()
	}()
	dec := json.NewDecoder(outR)
	send := func(s string) { io.WriteString(inW, s+"\n") }
	read := func() map[string]any {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	if m := read(); m["result"].(map[string]any)["protocolVersion"] != "2024-11-05" {
		t.Fatalf("initialize = %v", m)
	}
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"janetcode_task","arguments":{"prompt":"go","session_id":"old"},"_meta":{"progressToken":"p1"}}}`)
	prog := read()
	if prog["method"] != "notifications/progress" || prog["params"].(map[string]any)["message"] != "read_file main.go" {
		t.Fatalf("progress = %v", prog)
	}
	res := read()["result"].(map[string]any)
	sc := res["structuredContent"].(map[string]any)
	if sc["sessionId"] != "old" || sc["verdict"] != "✓ tested" {
		t.Fatalf("structured = %v", sc)
	}
	// A cancelled request ends the task it started.
	r.mu.Lock()
	r.block = make(chan struct{})
	r.mu.Unlock()
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"janetcode_task","arguments":{"prompt":"slow"}}}`)
	time.Sleep(50 * time.Millisecond)
	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`)
	done := make(chan map[string]any, 1)
	go func() { done <- read() }()
	select {
	case m := <-done:
		if m["result"].(map[string]any)["structuredContent"].(map[string]any)["stopReason"] != "cancelled" {
			t.Fatalf("cancelled task = %v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notifications/cancelled did not end the task")
	}
	send(`{"jsonrpc":"2.0","id":4,"method":"nope"}`)
	if m := read(); m["error"] == nil {
		t.Fatalf("unknown method = %v", m)
	}
	inW.Close()
}
