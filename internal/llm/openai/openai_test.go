package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func sse(lines ...string) string {
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString("data: " + l + "\n\n")
	}
	sb.WriteString("data: [DONE]\n\n")
	return sb.String()
}

func newTestProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL + "/v1/", APIKey: "k", MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewRequiresKey(t *testing.T) {
	if _, err := New(Config{}); err != llm.ErrNoAPIKey {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamTextAndToolCalls(t *testing.T) {
	var gotBody wireRequest
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`{"choices":[{"delta":{"reasoning_content":"think"}}]}`,
			`{"choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4},"cost":0.00123}}`,
		))
	})
	req := llm.Request{
		Model:  "m",
		System: "sys",
		Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "hi"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockText, Text: "ok"},
				{Type: llm.BlockToolUse, ID: "c0", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)},
			}},
			llm.ToolResultMessage(llm.ToolResult("c0", "/tmp", false), llm.ToolResult("c1", "boom", true)),
		},
		Tools:     []llm.ToolDef{{Name: "bash", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		MaxTokens: 100,
	}
	var events []llm.Event
	resp, err := p.Stream(context.Background(), req, func(e llm.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	// Request mapping.
	roles := []string{}
	for _, m := range gotBody.Messages {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,tool" {
		t.Fatalf("roles = %v", roles)
	}
	if gotBody.Messages[2].ToolCalls[0].Function.Arguments != `{"command":"pwd"}` {
		t.Fatalf("tool_calls = %+v", gotBody.Messages[2].ToolCalls)
	}
	if gotBody.Messages[4].Content != "Error: boom" || gotBody.Messages[4].ToolCallID != "c1" {
		t.Fatalf("tool msg = %+v", gotBody.Messages[4])
	}
	if !gotBody.Stream || gotBody.StreamOptions == nil || !gotBody.StreamOptions.IncludeUsage || gotBody.MaxTokens != 100 {
		t.Fatalf("stream opts wrong: %+v", gotBody)
	}
	if len(gotBody.Tools) != 1 || gotBody.Tools[0].Function.Name != "bash" {
		t.Fatalf("tools = %+v", gotBody.Tools)
	}
	// Response mapping.
	if resp.StopReason != llm.StopToolUse {
		t.Fatalf("stop = %s", resp.StopReason)
	}
	if resp.Usage != (llm.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 4, CostUSD: 0.00123}) {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	blocks := resp.Message.Blocks
	if len(blocks) != 4 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].Type != llm.BlockThinking || blocks[0].Text != "think" {
		t.Errorf("thinking = %+v", blocks[0])
	}
	if blocks[1].Type != llm.BlockText || blocks[1].Text != "Hello" {
		t.Errorf("text = %+v", blocks[1])
	}
	if blocks[2].ID != "call_1" || blocks[2].Name != "read_file" || string(blocks[2].Input) != `{"path":"a.go"}` {
		t.Errorf("tool0 = %+v", blocks[2])
	}
	if blocks[3].ID != "call_2" || string(blocks[3].Input) != `{"command":"ls"}` {
		t.Errorf("tool1 = %+v", blocks[3])
	}
	// Events: deltas streamed and tool starts/ends emitted.
	var text string
	starts, ends := 0, 0
	for _, e := range events {
		switch e.Type {
		case llm.EventTextDelta:
			text += e.Text
		case llm.EventToolUseStart:
			starts++
		case llm.EventToolUseEnd:
			ends++
		}
	}
	if text != "Hello" || starts != 2 || ends != 2 {
		t.Fatalf("events: text=%q starts=%d ends=%d", text, starts, ends)
	}
}

func TestStreamFinishLengthAndNoTools(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"x"},"finish_reason":"length"}]}`))
	})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != llm.StopMaxTokens || resp.Message.Text() != "x" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestHTTPErrorAndRetry(t *testing.T) {
	var calls int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil || resp.Message.Text() != "ok" || atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}

	p = newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, `{"error":{"message":"bad key"}}`)
	})
	_, err = p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	ae, ok := err.(*APIError)
	if !ok || ae.Status != 401 || ae.Message != "bad key" {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidToolJSONAndEmptyStream(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"bash","arguments":"{oops"}}]},"finish_reason":"tool_calls"}]}`))
	})
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("err = %v", err)
	}
	p = newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {})
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil); err == nil {
		t.Fatal("expected error for empty stream")
	}
}

func TestContextCancelDuringStream(t *testing.T) {
	block := make(chan struct{})
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-block
	})
	defer close(block)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { cancel() }()
	_, err := p.Stream(ctx, llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err == nil {
		t.Fatal("expected error on cancel")
	}
}

func TestDialectFallbacks(t *testing.T) {
	var bodies []map[string]any
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		switch {
		case b["max_tokens"] != nil:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."}}`)
		case b["stream_options"] != nil:
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`)
		default:
			io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
		}
	})
	req := llm.Request{Model: "m", MaxTokens: 50, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
	resp, err := p.Stream(context.Background(), req, nil)
	if err != nil || resp.Message.Text() != "ok" {
		t.Fatalf("err=%v resp=%+v", err, resp)
	}
	if len(bodies) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(bodies))
	}
	if bodies[1]["max_completion_tokens"].(float64) != 50 || bodies[1]["max_tokens"] != nil {
		t.Fatalf("second attempt = %v", bodies[1])
	}
	if bodies[2]["stream_options"] != nil {
		t.Fatalf("third attempt still has stream_options: %v", bodies[2])
	}
	// Learned quirks persist: the next call goes straight through.
	bodies = nil
	if _, err := p.Stream(context.Background(), req, nil); err != nil || len(bodies) != 1 {
		t.Fatalf("err=%v attempts=%d", err, len(bodies))
	}
	// An unrelated 400 is not retried.
	p2 := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"model not found"}}`)
	})
	if _, err := p2.Stream(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildRequestImages(t *testing.T) {
	w, err := buildRequest(llm.Request{Model: "m", Messages: []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "what is this"},
			{Type: llm.BlockImage, MediaType: "image/png", Data: "AAAA"},
		}},
		llm.TextMessage(llm.RoleUser, "plain"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(w.Messages)
	want := `[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},{"role":"user","content":"plain"}]`
	if string(raw) != want {
		t.Fatalf("messages = %s", raw)
	}
}

func TestKeyRotationAndListModels(t *testing.T) {
	var keysSeen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.URL.Path == "/v1/models" {
			if key != "k2" {
				w.WriteHeader(401)
				io.WriteString(w, `{"error":{"message":"bad key"}}`)
				return
			}
			io.WriteString(w, `{"data":[{"id":"zeta"},{"id":"alpha","name":"Alpha"}]}`)
			return
		}
		keysSeen = append(keysSeen, key)
		switch key {
		case "k1":
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"usage allocated quota exceeded"}}`)
		case "k2":
			io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
		default:
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"bad key"}}`)
		}
	}))
	defer srv.Close()
	p, err := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k1", APIKeys: []string{"k1", " k2 ", ""}, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.Keys() != 2 {
		t.Fatalf("keys = %d (dedupe and trim)", p.Keys())
	}
	req := llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
	resp, err := p.Stream(context.Background(), req, nil)
	if err != nil || resp.Message.Text() != "ok" {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	if strings.Join(keysSeen, ",") != "k1,k2" {
		t.Fatalf("keys tried = %v; the second key should be used right after the quota error", keysSeen)
	}
	// The working key stays current for later requests and model listing.
	keysSeen = nil
	if _, err := p.Stream(context.Background(), req, nil); err != nil || strings.Join(keysSeen, ",") != "k2" {
		t.Fatalf("second call keys = %v err = %v", keysSeen, err)
	}
	models, err := p.ListModels(context.Background())
	if err != nil || len(models) != 2 || models[0].ID != "alpha" || models[0].Name != "Alpha" || models[1].ID != "zeta" {
		t.Fatalf("models = %+v err = %v", models, err)
	}
	// A single key never loops: the 401 comes straight back.
	single, _ := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k9", MaxRetries: 1})
	if _, err := single.Stream(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("single key err = %v", err)
	}
}
