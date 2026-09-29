package acp

import (
	"encoding/json"
	"testing"
)

// A JetBrains IDE (AI Assistant's ACP support, configured in
// ~/.jetbrains/acp.json) opens with more than Zed does: terminal and
// _meta capabilities in initialize, and the IDE's own MCP servers —
// stdio and http — in session/new. None of it may be refused: the
// handshake has to come through with a session, and the turn run.
// The frames are the ones a JetBrains client sends, verified against a
// live run of `janetcode --acp` on 2026-09-29.
func TestJetBrainsHandshake(t *testing.T) {
	o, root := testOptions(t)
	c, stop := newClient(t, o)
	defer stop()

	c.call("initialize", json.RawMessage(`{"protocolVersion":1,
		"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true},"terminal":true,"_meta":{"terminal_output":true}},
		"clientInfo":{"name":"IntelliJ IDEA","title":"IntelliJ IDEA","version":"2026.2"}}`))
	m := c.read()
	if m.Error != nil {
		t.Fatalf("initialize refused: %+v", m.Error)
	}
	var init initializeResult
	if err := json.Unmarshal(m.Result, &init); err != nil {
		t.Fatal(err)
	}
	// An empty list, not null: the IDE offers no login when there is
	// nothing to log in to, and a null here is a schema violation.
	if init.ProtocolVersion != 1 || init.AuthMethods == nil {
		t.Fatalf("initialize = %s", m.Result)
	}

	params, _ := json.Marshal(map[string]any{
		"cwd": root,
		"mcpServers": []any{
			map[string]any{"name": "idea", "command": "/opt/idea/bin/mcp", "args": []string{}, "env": []any{}},
			map[string]any{"type": "http", "name": "web", "url": "http://127.0.0.1:1/mcp", "headers": []any{}},
		},
	})
	c.call("session/new", json.RawMessage(params))
	m = c.read()
	if m.Error != nil {
		t.Fatalf("session/new with the IDE's MCP servers refused: %+v", m.Error)
	}
	var ns newSessionResult
	if err := json.Unmarshal(m.Result, &ns); err != nil || ns.SessionID == "" {
		t.Fatalf("session/new = %s (%v)", m.Result, err)
	}

	c.call("session/prompt", promptParams{SessionID: ns.SessionID, Prompt: []contentBlock{{Type: "text", Text: "write out.txt"}}})
	final, _ := c.readUntil(isResponse)
	var pr promptResult
	if err := json.Unmarshal(final.Result, &pr); err != nil || pr.StopReason != stopEndTurn {
		t.Fatalf("prompt = %s", final.Result)
	}
}
