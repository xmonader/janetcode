package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	if resp.Usage != (llm.Usage{InputTokens: 6, OutputTokens: 5, CacheReadTokens: 4, CostUSD: 0.00123}) {
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

// A server that rejects prompt_cache_key must be worked around on the same
// turn, like every other quirk: learning the flag and then failing loses
// the user's turn to a 400 the code knows how to survive.
func TestDialectDropsPromptCacheKey(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		if b["prompt_cache_key"] != nil {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unrecognized request argument supplied: prompt_cache_key"}}`)
			return
		}
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL + "/v1/", APIKey: "k", MaxRetries: 2, CacheKey: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	req := llm.Request{Model: "m", MaxTokens: 50, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
	resp, err := p.Stream(context.Background(), req, nil)
	if err != nil || resp.Message.Text() != "ok" {
		t.Fatalf("err=%v resp=%+v", err, resp)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected the request to be resent without the key, got %d attempts", len(bodies))
	}
	if bodies[1]["prompt_cache_key"] != nil {
		t.Fatalf("the resend still carries the rejected key: %v", bodies[1])
	}
	// Learned: the next call goes straight through without it.
	bodies = nil
	if _, err := p.Stream(context.Background(), req, nil); err != nil || len(bodies) != 1 {
		t.Fatalf("err=%v attempts=%d", err, len(bodies))
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
			io.WriteString(w, `{"data":[{"id":"zeta","context_length":32000,"pricing":{"prompt":"0.00000015","completion":"0.0000006","input_cache_read":"0.00000005"}},{"id":"alpha","name":"Alpha","top_provider":{"context_length":8192,"max_completion_tokens":1024},"pricing":{"prompt":"-1","completion":"-1"}}]}`)
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
	if err == nil && len(models) == 2 {
		if a, z := models[0], models[1]; a.ContextWindow != 8192 || a.MaxOutput != 1024 || a.Price != nil || z.ContextWindow != 32000 || z.Price == nil || z.Price.Input != 0.15 || z.Price.Output != 0.6 || z.Price.CacheRead != 0.05 {
			t.Fatalf("metadata: alpha=%+v zeta=%+v", a, z)
		}
	}
	if err != nil || len(models) != 2 || models[0].ID != "alpha" || models[0].Name != "Alpha" || models[1].ID != "zeta" {
		t.Fatalf("models = %+v err = %v", models, err)
	}
	// A single key never loops: the 401 comes straight back.
	single, _ := New(Config{BaseURL: srv.URL + "/v1", APIKey: "k9", MaxRetries: 1})
	if _, err := single.Stream(context.Background(), req, nil); err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("single key err = %v", err)
	}
}

func TestLocalServerNeedsNoKey(t *testing.T) {
	var auth string
	var gotAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, gotAuth = r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"llama3"}]}`))
	}))
	defer srv.Close()
	p, err := New(Config{BaseURL: srv.URL}) // httptest listens on 127.0.0.1
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "llama3" {
		t.Fatalf("models = %v, %v", models, err)
	}
	if gotAuth {
		t.Fatalf("sent Authorization %q without a key", auth)
	}
	if _, err := New(Config{BaseURL: "https://api.example.com/v1"}); err != llm.ErrNoAPIKey {
		t.Fatalf("remote without key: err = %v", err)
	}
}

func TestDeadKeyIsSkippedOnLaterRequests(t *testing.T) {
	var keysSeen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		keysSeen = append(keysSeen, key)
		if key == "dead" {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"invalid api key"}}`)
			return
		}
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	p, err := New(Config{BaseURL: srv.URL + "/v1", APIKeys: []string{"dead", "live"}, MaxRetries: 1})
	if err != nil {
		t.Fatal(err)
	}
	req := llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
	for i := 0; i < 3; i++ {
		if _, err := p.Stream(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
	}
	// The rejected key is tried once, then skipped for an hour.
	if strings.Join(keysSeen, ",") != "dead,live,live,live" {
		t.Fatalf("keys tried = %v", keysSeen)
	}
}

func TestEmbed(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(404)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`)
	}))
	defer srv.Close()
	p, err := New(Config{APIKeys: []string{"k"}, BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := p.Embed(context.Background(), "emb", []string{"a", "b"})
	if err != nil || len(vecs) != 2 || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("vecs=%v err=%v", vecs, err)
	}
	if got["model"] != "emb" || len(got["input"].([]any)) != 2 {
		t.Fatalf("request = %v", got)
	}
	if _, err := p.Embed(context.Background(), "emb", []string{"a", "b", "c"}); err == nil {
		t.Fatal("count mismatch accepted")
	}
}

func TestCacheableURLAndDashScopeHeader(t *testing.T) {
	if !cacheableURL("https://coding-intl.dashscope.aliyuncs.com/v1") || !cacheableURL("https://openrouter.ai/api/v1") {
		t.Fatal("dashscope and openrouter must be cacheable")
	}
	if cacheableURL("http://127.0.0.1:11434/v1") || cacheableURL("https://api.openai.com/v1") {
		t.Fatal("local and openai.com must not get cache_control arrays")
	}
	h := extraHeaders("https://coding-intl.dashscope.aliyuncs.com/v1")
	if h["X-DashScope-CacheControl"] != "enable" {
		t.Fatalf("dashscope header = %v", h)
	}
	if extraHeaders("https://openrouter.ai/api/v1") != nil {
		t.Fatal("openrouter should not get the DashScope header")
	}
}

func TestApplyCacheBreakpoints(t *testing.T) {
	req := llm.Request{
		Model:  "qwen3.7-plus",
		System: "you are a coding agent\n\n# Environment\n- Working directory: /tmp/eval-abc\n",
		Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "first"),
			llm.TextMessage(llm.RoleAssistant, "ok"),
			llm.TextMessage(llm.RoleUser, "tail that changes"),
		},
		Tools: []llm.ToolDef{{Name: "bash", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	w, err := buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	applyCacheBreakpoints(&w)
	sys, ok := w.Messages[0].Content.([]wirePart)
	if !ok || len(sys) != 2 {
		t.Fatalf("system should split static/env: %#v", w.Messages[0].Content)
	}
	if sys[0].CacheControl == nil || sys[0].CacheControl.Type != "ephemeral" || strings.Contains(sys[0].Text, "Working directory") {
		t.Fatalf("static system block = %#v", sys[0])
	}
	if sys[1].CacheControl != nil || !strings.Contains(sys[1].Text, "Working directory") {
		t.Fatalf("env block must be uncached: %#v", sys[1])
	}
	if w.Tools[0].CacheControl == nil || w.Tools[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("last tool must be marked: %+v", w.Tools[0])
	}
	mid, ok := w.Messages[2].Content.([]wirePart) // assistant, not the tail user
	if !ok || mid[0].CacheControl == nil {
		t.Fatalf("stable prefix breakpoint missing: %+v", w.Messages)
	}
	tail, ok := w.Messages[3].Content.([]wirePart)
	if !ok || tail[0].CacheControl != nil {
		t.Fatalf("tail must not be marked: %#v", w.Messages[3].Content)
	}
}

func TestDashScopeStreamSendsCacheHeaderAndBreakpoints(t *testing.T) {
	var gotHeader string
	var gotBody wireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-DashScope-CacheControl")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL + "/dashscope/v1", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Stream(context.Background(), llm.Request{
		Model:    "qwen3.7-plus",
		System:   "sys",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotHeader != "enable" {
		t.Fatalf("header = %q", gotHeader)
	}
	sys, _ := gotBody.Messages[0].Content.([]any)
	if len(sys) == 0 {
		t.Fatalf("system content not array: %#v", gotBody.Messages[0].Content)
	}
}

func TestRequestCarriesTheSchema(t *testing.T) {
	w, err := buildRequest(llm.Request{
		Model: "m", MaxTokens: 10,
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")},
		Schema:   &llm.Schema{Name: "working_memory", Strict: true, JSON: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.ResponseFormat == nil || w.ResponseFormat.Type != "json_schema" {
		t.Fatalf("response_format = %+v", w.ResponseFormat)
	}
	if w.ResponseFormat.JSONSchema.Name != "working_memory" || !w.ResponseFormat.JSONSchema.Strict {
		t.Fatalf("json_schema = %+v", w.ResponseFormat.JSONSchema)
	}
	// Without a schema the field is absent, so a server that has never
	// heard of it sees the request it saw before.
	plain, _ := buildRequest(llm.Request{Model: "m", MaxTokens: 10, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}})
	if plain.ResponseFormat != nil {
		t.Fatalf("unrequested response_format: %+v", plain.ResponseFormat)
	}
	b, _ := json.Marshal(plain)
	if strings.Contains(string(b), "response_format") {
		t.Fatalf("field serialised anyway: %s", b)
	}
}

// A server that rejects the field is learned from and the call is retried
// without it, since the reply is parsed either way.
func TestLearnDialectDropsJSONSchema(t *testing.T) {
	p := &Provider{}
	w := wireRequest{ResponseFormat: &wireResponseFormat{Type: "json_schema"}}
	if got := p.applyDialect(w); got.ResponseFormat == nil {
		t.Fatal("dropped before the server complained")
	}
	if !p.learnDialect(&APIError{Status: 400, Message: "unknown parameter: response_format"}) {
		t.Fatal("the complaint was not recognised")
	}
	if got := p.applyDialect(w); got.ResponseFormat != nil {
		t.Fatalf("still sent after learning: %+v", got.ResponseFormat)
	}
	// Learning it once is enough; the same complaint does not loop.
	if p.learnDialect(&APIError{Status: 400, Message: "unknown parameter: response_format"}) {
		t.Fatal("retried a second time for the same reason")
	}
}

// TestReadStreamSeparatesToolCallsWithoutIndex reproduces a real failure:
// a provider that streams two tool calls without an "index" field on the
// deltas had both of them accumulate into one, producing the tool name
// "read_fileedit_file" and two JSON objects glued together as its
// arguments. The whole turn died on it.
func TestReadStreamSeparatesToolCallsWithoutIndex(t *testing.T) {
	// Each chunk carries one tool-call delta and no index, which is what
	// made the old position-in-chunk fallback collapse them.
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_a","type":"function","function":{"name":"read_file","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"path\":\"a.go\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"id":"call_b","type":"function","function":{"name":"edit_file","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"path\":\"b.go\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")

	resp, err := readStream(strings.NewReader(b.String()), nil, func(llm.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	uses := resp.Message.ToolUses()
	if len(uses) != 2 {
		t.Fatalf("got %d tool calls, want 2: %+v", len(uses), uses)
	}
	if uses[0].Name != "read_file" || string(uses[0].Input) != `{"path":"a.go"}` {
		t.Fatalf("first call is %q %s", uses[0].Name, uses[0].Input)
	}
	if uses[1].Name != "edit_file" || string(uses[1].Input) != `{"path":"b.go"}` {
		t.Fatalf("second call is %q %s", uses[1].Name, uses[1].Input)
	}
	if uses[0].ID == uses[1].ID {
		t.Fatalf("both calls share the id %q", uses[0].ID)
	}
}

// TestReadStreamProviderQuirks drives the shapes real providers actually
// send. The tool-call merge bug was found by a benchmark failing, not by a
// test, so these are the neighbouring cases that would have failed the
// same way.
func TestReadStreamProviderQuirks(t *testing.T) {
	stream := func(chunks ...string) string {
		var b strings.Builder
		for _, c := range chunks {
			b.WriteString("data: " + c + "\n\n")
		}
		b.WriteString("data: [DONE]\n\n")
		return b.String()
	}
	type want struct {
		names []string
		args  []string
		text  string
	}
	cases := []struct {
		name   string
		chunks []string
		want   want
	}{{
		name: "two calls in one chunk, no index",
		chunks: []string{
			`{"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}},{"id":"b","function":{"name":"grep","arguments":"{\"pattern\":\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		},
		want: want{names: []string{"read_file", "grep"}, args: []string{`{"path":"a"}`, `{"pattern":"x"}`}},
	}, {
		name: "indices arrive out of order",
		chunks: []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"grep","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read_file","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"pattern\":\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		},
		// The index decides the order, not the order the deltas arrived in.
		want: want{names: []string{"read_file", "grep"}, args: []string{`{"path":"a"}`, `{"pattern":"x"}`}},
	}, {
		name: "no ids at all, name then arguments",
		chunks: []string{
			`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"read_file"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"path\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"a\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"grep","arguments":"{\"pattern\":\"x\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		},
		want: want{names: []string{"read_file", "grep"}, args: []string{`{"path":"a"}`, `{"pattern":"x"}`}},
	}, {
		name: "a function name split across deltas",
		chunks: []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read_"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"file","arguments":"{\"path\":\"a\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		},
		want: want{names: []string{"read_file"}, args: []string{`{"path":"a"}`}},
	}, {
		name: "text alongside a tool call",
		chunks: []string{
			`{"choices":[{"delta":{"content":"Let me look. "}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		},
		want: want{names: []string{"read_file"}, args: []string{`{"path":"a"}`}, text: "Let me look. "},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := readStream(strings.NewReader(stream(tc.chunks...)), nil, func(llm.Event) {})
			if err != nil {
				t.Fatalf("readStream: %v", err)
			}
			uses := resp.Message.ToolUses()
			if len(uses) != len(tc.want.names) {
				t.Fatalf("got %d calls, want %d: %+v", len(uses), len(tc.want.names), uses)
			}
			for i, u := range uses {
				if u.Name != tc.want.names[i] {
					t.Errorf("call %d is %q, want %q", i, u.Name, tc.want.names[i])
				}
				if string(u.Input) != tc.want.args[i] {
					t.Errorf("call %d args %s, want %s", i, u.Input, tc.want.args[i])
				}
				if u.ID == "" {
					t.Errorf("call %d has no id; a tool result cannot be matched to it", i)
				}
			}
			if tc.want.text != "" && resp.Message.Text() != tc.want.text {
				t.Errorf("text %q, want %q", resp.Message.Text(), tc.want.text)
			}
		})
	}
}

// TestReadStreamHostileFrames covers two failure shapes found by reading
// other harnesses' scar tissue: a provider that reuses index 0 for a
// second, different tool call, and an empty SSE data frame used as a
// keep-alive. Both are reported to kill turns in the wild.
func TestReadStreamHostileFrames(t *testing.T) {
	t.Run("index 0 reused for a second call", func(t *testing.T) {
		s := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}]}}]}` + "\n\n" +
			"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_b","function":{"name":"grep","arguments":"{\"pattern\":\"x\"}"}}]}}]}` + "\n\n" +
			"data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
			"data: [DONE]\n\n"
		resp, err := readStream(strings.NewReader(s), nil, func(llm.Event) {})
		if err != nil {
			t.Fatalf("readStream: %v", err)
		}
		uses := resp.Message.ToolUses()
		if len(uses) != 2 {
			t.Fatalf("got %d calls, want 2 (the second reused index 0): %+v", len(uses), uses)
		}
		if uses[0].Name != "read_file" || uses[1].Name != "grep" {
			t.Fatalf("got %q and %q", uses[0].Name, uses[1].Name)
		}
	})

	t.Run("empty data frame is a keep-alive, not a parse error", func(t *testing.T) {
		s := "data:\n\n" +
			"data: " + `{"choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
			"data:  \n\n" +
			"data: " + `{"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
			"data: [DONE]\n\n"
		resp, err := readStream(strings.NewReader(s), nil, func(llm.Event) {})
		if err != nil {
			t.Fatalf("an empty keep-alive frame must not fail the stream: %v", err)
		}
		if resp.Message.Text() != "hi" {
			t.Fatalf("text %q", resp.Message.Text())
		}
	})
}

// TestReadStreamWholeArgumentResend covers providers that resend the whole
// argument string on every delta instead of sending true deltas. Appending
// them yields {"path":"a"}{"path":"a"} and the turn dies, so a chunk that
// restarts a JSON value replaces what came before it.
func TestReadStreamWholeArgumentResend(t *testing.T) {
	s := "data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a.go\"}"}}]}}]}` + "\n\n" +
		"data: " + `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	resp, err := readStream(strings.NewReader(s), nil, func(llm.Event) {})
	if err != nil {
		t.Fatalf("readStream: %v", err)
	}
	uses := resp.Message.ToolUses()
	if len(uses) != 1 {
		t.Fatalf("got %d calls", len(uses))
	}
	if got := string(uses[0].Input); got != `{"path":"a.go"}` {
		t.Fatalf("arguments %s — a resent whole value must replace, not append", got)
	}
}

// TestPromptCacheKey: a session's growing prefix must keep hitting the
// same cache shard, and the field must not be sent to servers that would
// reject it.
func TestPromptCacheKey(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://api.openai.com/v1", true},
		{"https://openrouter.ai/api/v1", true},
		{"http://localhost:11434/v1", false},
		{"https://coding-intl.dashscope.aliyuncs.com/v1", false},
	} {
		p, err := New(Config{BaseURL: tc.url, APIKey: "k"})
		if err != nil {
			t.Fatalf("%s: %v", tc.url, err)
		}
		if got := p.cfg.CacheKey != ""; got != tc.want {
			t.Errorf("%s: cache key present = %v, want %v", tc.url, got, tc.want)
		}
	}
	// Stable across calls within one provider, distinct between sessions.
	a, _ := New(Config{BaseURL: "https://api.openai.com/v1", APIKey: "k"})
	b, _ := New(Config{BaseURL: "https://api.openai.com/v1", APIKey: "k"})
	if a.cfg.CacheKey == "" || a.cfg.CacheKey == b.cfg.CacheKey {
		t.Fatalf("keys should exist and differ per session: %q %q", a.cfg.CacheKey, b.cfg.CacheKey)
	}
	// An explicit key wins.
	c, _ := New(Config{BaseURL: "https://api.openai.com/v1", APIKey: "k", CacheKey: "mine"})
	if c.cfg.CacheKey != "mine" {
		t.Fatalf("explicit key overridden: %q", c.cfg.CacheKey)
	}
}

// TestRetryableDistinguishesExhaustedFromThrottled: both arrive as 429,
// but retrying an exhausted account only buries the real reason under a
// backoff that was never going to help.
func TestRetryableDistinguishesExhaustedFromThrottled(t *testing.T) {
	for _, tc := range []struct {
		status int
		msg    string
		retry  bool
	}{
		{429, "Rate limit reached for gpt-4 in organization org-x", true},
		{429, "You exceeded your current quota, please check your plan and billing details", false},
		{429, "Your credit balance is too low to access the API", false},
		{429, "insufficient_quota", false},
		{529, "Overloaded", true},
		{503, "upstream unavailable", true},
		{400, "bad request", false},
		{401, "invalid api key", false},
	} {
		err := &APIError{Status: tc.status, Message: tc.msg}
		if got := retryable(err); got != tc.retry {
			t.Errorf("status %d %q: retryable = %v, want %v", tc.status, tc.msg, got, tc.retry)
		}
	}
}

// TestRetryAnnouncesItself: a stalled call must not look like a slow one,
// so every backoff says what it is waiting for and for how long.
func TestRetryAnnouncesItself(t *testing.T) {
	var calls int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"slow down"}}`)
			return
		}
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	})
	var status []string
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, func(e llm.Event) {
		if e.Type == llm.EventStatus {
			status = append(status, e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 2 {
		t.Fatalf("status events = %v", status)
	}
	if !strings.HasPrefix(status[0], "retrying 1/") || !strings.Contains(status[0], "rate limited") || !strings.Contains(status[0], " in ") {
		t.Fatalf("status[0] = %q", status[0])
	}
}

// A stream that goes quiet mid-answer must end as a failed call rather
// than hang the turn for as long as the process lives.
func TestStreamIdleWatchdog(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-release // never writes another frame
	}))
	defer srv.Close()
	defer close(release)
	p, err := New(Config{BaseURL: srv.URL + "/v1/", APIKey: "k", Limits: llm.StreamLimits{Idle: 50 * time.Millisecond, Max: -1}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if !errors.Is(err, llm.ErrStreamWatchdog) {
		t.Fatalf("err = %v, want a stalled-stream error", err)
	}
}

// A stream that keeps producing past the lifetime cap is still stopped.
func TestStreamLifetimeWatchdog(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			select {
			case <-release:
				return
			default:
			}
			if _, err := io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer srv.Close()
	defer close(release)
	p, err := New(Config{BaseURL: srv.URL + "/v1/", APIKey: "k", Limits: llm.StreamLimits{Idle: -1, Max: 80 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	var st *llm.StreamStalled
	if !errors.As(err, &st) || !st.Lifetime {
		t.Fatalf("err = %v, want the lifetime cap", err)
	}
}
