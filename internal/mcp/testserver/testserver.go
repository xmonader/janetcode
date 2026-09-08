// Package testserver is a tiny MCP server used by tests in other packages.
// It covers the protocol subset the mcp package's own tests exercise.
package testserver

import (
	"bufio"
	"encoding/json"
	"os"
)

type msg struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
	Params json.RawMessage  `json:"params,omitempty"`
}

// Run serves stdin/stdout until EOF. It offers four tools: echo (read-only),
// "write thing!", fail and slow.
func Run() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<16), 16<<20)
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	for in.Scan() {
		var m msg
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
				"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "fake", "version": "0.1"},
			}})
		case "tools/list":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"tools": []map[string]any{
				{"name": "echo", "description": "Echo text back", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}, "annotations": map[string]any{"readOnlyHint": true}},
				{"name": "write thing!", "inputSchema": map[string]any{"type": "object"}},
				{"name": "fail", "inputSchema": map[string]any{"type": "object"}},
				{"name": "slow", "inputSchema": map[string]any{"type": "object"}},
			}}})
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			json.Unmarshal(m.Params, &p)
			var a struct{ Text string }
			json.Unmarshal(p.Arguments, &a)
			if p.Name == "fail" {
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "it broke"}}, "isError": true}})
				continue
			}
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "echo: " + a.Text}}}})
		}
	}
}
