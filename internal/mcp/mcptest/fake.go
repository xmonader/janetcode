package mcptest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type fakeMessage struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
	Result json.RawMessage  `json:"result,omitempty"`
}

// FakeServer serves a scripted MCP server on stdin/stdout until EOF. A
// test binary runs it from TestMain when it re-executes itself as the
// server, so the client is exercised over a real pipe and process
// boundary. It offers paginated tools (echo read-only, "write thing!",
// fail, slow), resources and a template, a review prompt, and sends the
// client a ping and a roots/list request after the handshake.
func FakeServer() {
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
	// MCP_FAKE_SERVER_DELAY stands in for a server that takes a while to
	// come up — a cold npx, a driver probing the machine — so a test can
	// land a shutdown squarely inside the handshake.
	if d, err := time.ParseDuration(os.Getenv("MCP_FAKE_SERVER_DELAY")); err == nil && d > 0 {
		time.Sleep(d)
	}
	pingSent := false
	for in.Scan() {
		var msg fakeMessage
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
