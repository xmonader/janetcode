package sdk_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/sdk"
)

// TestMain lets the test binary stand in for janetcode --acp when
// SDK_FAKE_ACP is set: a scripted agent whose stream a real model cannot
// be made to produce on demand.
func TestMain(m *testing.M) {
	if os.Getenv("SDK_FAKE_ACP") == "1" {
		fakeACP()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeACP() {
	in := bufio.NewScanner(os.Stdin)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	update := func(u map[string]any) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s1", "update": u}})
	}
	notice := map[string]any{"janetcode": map[string]any{"notice": true}}
	discard := map[string]any{"janetcode": map[string]any{"notice": true, "discard": true}}
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.Unmarshal(in.Bytes(), &m)
		switch m.Method {
		case "initialize":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "janetcode", "version": "fake"}}})
		case "session/new":
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"sessionId": "s1"}})
		case "session/prompt":
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "msg-1", "content": map[string]any{"type": "text", "text": "Hello wor"}})
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "\n_(cut off, resent)_\n"}, "_meta": discard})
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "msg-2", "content": map[string]any{"type": "text", "text": "Hello world."}})
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "\n_memory recalled: x_\n"}, "_meta": notice})
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"stopReason": "end_turn", "_meta": map[string]any{"janetcode": map[string]any{"verdict": "✓ ok", "usage": map[string]any{"inputTokens": 3}}}}})
		}
	}
}

// A notice is the harness speaking and is not in the answer; a stream
// cut off and resent leaves only the resent text.
func TestNoticesAndDiscardsStayOutOfTheAnswer(t *testing.T) {
	var notices []string
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := sdk.Run(ctx, sdk.Options{Binary: os.Args[0], Dir: t.TempDir(), Env: []string{"SDK_FAKE_ACP=1"},
		OnEvent: func(e sdk.Event) {
			if e.Kind == sdk.EventNotice {
				notices = append(notices, e.Text)
			}
		}}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello world." {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Verdict != "✓ ok" || res.Usage.InputTokens != 3 {
		t.Fatalf("result = %+v", res)
	}
	if len(notices) != 2 || !strings.Contains(notices[1], "memory recalled") {
		t.Fatalf("notices = %q", notices)
	}
}
