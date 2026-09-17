package mcptest

import (
	"bufio"
	"encoding/json"
	"os"
)

// EnvServer is a minimal stdio MCP server whose single "env" tool returns
// the value of one of its own environment variables. Tests use it to prove
// the client scrubs credentials from a spawned server's environment: the
// parent sets a marker variable, calls env, and asserts the marker never
// crosses the pipe boundary.
func EnvServer() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<16), 16<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	for in.Scan() {
		var msg fakeMessage
		if err := json.Unmarshal(in.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "envprobe", "version": "0.1"},
			}})
		case "tools/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"tools": []map[string]any{{
					"name":        "env",
					"description": "Return one environment variable's value",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}, "required": []string{"name"}},
				}},
			}})
		case "tools/call":
			var p struct {
				Arguments struct {
					Name string `json:"name"`
				} `json:"arguments"`
			}
			json.Unmarshal(msg.Params, &p)
			val := os.Getenv(p.Arguments.Name)
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": p.Arguments.Name + "=" + val}},
			}})
		}
	}
}
