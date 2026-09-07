package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func event(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

func newTestProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL, Thinking: true, Effort: "high", MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{}); err != llm.ErrNoAPIKey {
		t.Fatalf("err = %v", err)
	}
	if _, err := New(Config{APIKey: "k", Effort: "ultra"}); err == nil {
		t.Fatal("expected invalid effort error")
	}
}

func TestStreamMapsRequestAndResponse(t *testing.T) {
	var body map[string]any
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("missing api key header")
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":20,"output_tokens":1,"cache_read_input_tokens":8,"cache_creation_input_tokens":2}}}`))
		io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`))
		io.WriteString(w, event("content_block_stop", `{"type":"content_block_stop","index":0}`))
		io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hel"}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo"}}`))
		io.WriteString(w, event("content_block_stop", `{"type":"content_block_stop","index":1}`))
		io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{}}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a.go\"}"}}`))
		io.WriteString(w, event("content_block_stop", `{"type":"content_block_stop","index":2}`))
		io.WriteString(w, event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":15}}`))
		io.WriteString(w, event("message_stop", `{"type":"message_stop"}`))
	})
	req := llm.Request{
		Model:  "claude-opus-5",
		System: "You are a coding agent.",
		Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "hi"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockThinking, Text: "t", Signature: "s"},
				{Type: llm.BlockThinking, Text: "unsigned from another provider"},
				{Type: llm.BlockText, Text: "ok"},
				{Type: llm.BlockToolUse, ID: "c0", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)},
			}},
			llm.ToolResultMessage(llm.ToolResult("c0", "", true)),
		},
		Tools:     []llm.ToolDef{{Name: "bash", Description: "d", InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`)}},
		MaxTokens: 100,
	}
	var events []llm.Event
	resp, err := p.Stream(context.Background(), req, func(e llm.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	// Request body checks.
	if body["model"] != "claude-opus-5" || body["max_tokens"].(float64) != 100 {
		t.Errorf("model/max_tokens wrong: %v", body)
	}
	sys := body["system"].([]any)[0].(map[string]any)
	if sys["text"] != "You are a coding agent." || sys["cache_control"] == nil {
		t.Errorf("system = %v", sys)
	}
	if th := body["thinking"].(map[string]any); th["type"] != "adaptive" || th["display"] != "summarized" {
		t.Errorf("thinking = %v", th)
	}
	if oc := body["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Errorf("output_config = %v", oc)
	}
	tools := body["tools"].([]any)
	schema := tools[0].(map[string]any)["input_schema"].(map[string]any)
	if schema["type"] != "object" || schema["additionalProperties"] != false || schema["required"].([]any)[0] != "command" {
		t.Errorf("schema = %v", schema)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	asst := msgs[1].(map[string]any)["content"].([]any)
	if len(asst) != 3 {
		t.Fatalf("assistant content = %v (unsigned thinking should be dropped)", asst)
	}
	if asst[0].(map[string]any)["type"] != "thinking" || asst[0].(map[string]any)["signature"] != "s" {
		t.Errorf("thinking block = %v", asst[0])
	}
	if tu := asst[2].(map[string]any); tu["type"] != "tool_use" || tu["input"].(map[string]any)["command"] != "pwd" {
		t.Errorf("tool_use = %v", tu)
	}
	tr := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if tr["type"] != "tool_result" || tr["is_error"] != true || tr["tool_use_id"] != "c0" {
		t.Errorf("tool_result = %v", tr)
	}
	// Response checks.
	if resp.StopReason != llm.StopToolUse {
		t.Errorf("stop = %s", resp.StopReason)
	}
	if resp.Usage != (llm.Usage{InputTokens: 20, OutputTokens: 15, CacheReadTokens: 8, CacheWriteTokens: 2}) {
		t.Errorf("usage = %+v", resp.Usage)
	}
	b := resp.Message.Blocks
	if len(b) != 3 {
		t.Fatalf("blocks = %+v", b)
	}
	if b[0].Type != llm.BlockThinking || b[0].Text != "plan" || b[0].Signature != "sig123" {
		t.Errorf("thinking = %+v", b[0])
	}
	if b[1].Type != llm.BlockText || b[1].Text != "Hello" {
		t.Errorf("text = %+v", b[1])
	}
	if b[2].Type != llm.BlockToolUse || b[2].ID != "toolu_1" || b[2].Name != "read_file" || string(b[2].Input) != `{"path":"a.go"}` {
		t.Errorf("tool = %+v %s", b[2], b[2].Input)
	}
	var text, thinking, partial string
	var start, end *llm.Block
	for _, e := range events {
		switch e.Type {
		case llm.EventTextDelta:
			text += e.Text
		case llm.EventThinkingDelta:
			thinking += e.Text
		case llm.EventToolUseDelta:
			partial += e.Text
		case llm.EventToolUseStart:
			start = e.Block
		case llm.EventToolUseEnd:
			end = e.Block
		}
	}
	if text != "Hello" || thinking != "plan" || partial != `{"path":"a.go"}` {
		t.Errorf("deltas: text=%q thinking=%q partial=%q", text, thinking, partial)
	}
	if start == nil || start.Name != "read_file" || end == nil || string(end.Input) != `{"path":"a.go"}` {
		t.Errorf("tool events: start=%+v end=%+v", start, end)
	}
}

func TestStreamAPIError(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	})
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	ae, ok := err.(*APIError)
	if !ok || ae.Status != 401 || !strings.Contains(ae.Message, "invalid x-api-key") {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamRefusalAndEndTurn(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
		io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"no"}}`))
		io.WriteString(w, event("content_block_stop", `{"type":"content_block_stop","index":0}`))
		io.WriteString(w, event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber"}},"usage":{"output_tokens":2}}`))
		io.WriteString(w, event("message_stop", `{"type":"message_stop"}`))
	})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != llm.StopRefusal || resp.Message.Text() != "no" {
		t.Fatalf("resp = %+v", resp)
	}
}
