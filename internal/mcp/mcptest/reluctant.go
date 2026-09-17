package mcptest

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ReluctantServer answers the handshake, then stops reading stdin for a
// while and starts again. It is the server that catches a *late* write:
// bounding a pipe write by returning on ctx.Done left the write parked,
// so a request the caller had already been told had failed was delivered
// when the server resumed — and run.
//
// Every tools/call it processes leaves a file in $MCP_RAN_DIR, so a test
// can ask whether a cancelled call was executed after the fact.
func ReluctantServer() {
	ranDir := os.Getenv("MCP_RAN_DIR")
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
				"serverInfo":      map[string]any{"name": "reluctant", "version": "0.1"},
			}})
		case "tools/list":
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"tools": []map[string]any{{
					"name":        "ship",
					"description": "Does something that must happen at most once",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
				}},
			}})
			// Handshake done: stop draining stdin, so the client's next
			// sizable request fills the 64 KB pipe and blocks.
			time.Sleep(2 * time.Second)
		case "tools/call":
			if ranDir != "" {
				os.WriteFile(filepath.Join(ranDir, "ran"), []byte("the tool ran"), 0o644)
			}
			send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "shipped"}},
			}})
		}
	}
}
