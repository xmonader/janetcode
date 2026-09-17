package mcptest

import (
	"bufio"
	"encoding/json"
	"os"
)

// StickyServer answers the handshake (initialize, tools/list) and then
// stops reading stdin forever. A large tools/call then fills the client's
// pipe and blocks its write — the condition that send's context bound
// exists to unwind instead of hanging the caller.
func StickyServer() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<16), 16<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	servedList := false
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
				"serverInfo":      map[string]any{"name": "sticky", "version": "0.1"},
			}})
		case "tools/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"tools": []map[string]any{{
					"name":        "echo",
					"description": "Echo text back",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
				}},
			}})
			servedList = true
		}
		if servedList {
			// Handshake complete: from here the server never drains
			// stdin again.
			select {}
		}
	}
}
