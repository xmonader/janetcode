package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

type server struct {
	t      *testing.T
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
	keys   []string
	reply  []func(w http.ResponseWriter)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	i := len(s.bodies)
	s.bodies = append(s.bodies, body)
	s.paths = append(s.paths, r.URL.RequestURI())
	s.keys = append(s.keys, r.Header.Get("x-goog-api-key"))
	s.mu.Unlock()
	if i >= len(s.reply) {
		s.t.Errorf("unexpected request %d", i+1)
		w.WriteHeader(500)
		return
	}
	s.reply[i](w)
}

func frames(fs ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range fs {
			fmt.Fprintf(w, "data: %s\r\n\r\n", f)
		}
	}
}

func newTest(t *testing.T, replies ...func(w http.ResponseWriter)) (*Provider, *server) {
	t.Helper()
	s := &server{t: t, reply: replies}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL + "/v1beta", APIKeys: []string{"secret"}, Thoughts: true})
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}

func TestStreamsTextThoughtsAndASignedCall(t *testing.T) {
	p, s := newTest(t, frames(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"weighing it","thought":true}]}}],"modelVersion":"gemini-3-pro"}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"Let me look."}]}}]}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read_file","args":{"path":"a.go"}},"thoughtSignature":"c2lnMQ=="},{"functionCall":{"id":"fc-2","name":"glob","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":40,"candidatesTokenCount":20,"thoughtsTokenCount":7}}`,
	))
	var events []llm.Event
	resp, err := p.Stream(context.Background(), llm.Request{
		Model: "gemini-3-pro", System: "be brief", MaxTokens: 1000, Effort: "low",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "read a.go")},
		Tools:    []llm.ToolDef{{Name: "read_file", Description: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`)}},
	}, func(e llm.Event) { events = append(events, e) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != llm.StopToolUse || resp.Model != "gemini-3-pro" {
		t.Fatalf("stop=%s model=%s", resp.StopReason, resp.Model)
	}
	want := llm.Usage{InputTokens: 60, CacheReadTokens: 40, OutputTokens: 27, ReasoningTokens: 7}
	if resp.Usage != want {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	bs := resp.Message.Blocks
	if len(bs) != 4 || bs[0].Type != llm.BlockThinking || bs[1].Text != "Let me look." {
		t.Fatalf("blocks = %+v", bs)
	}
	if bs[2].Name != "read_file" || bs[2].Signature != "c2lnMQ==" || string(bs[2].Input) != `{"path":"a.go"}` || !strings.HasPrefix(bs[2].ID, callPrefix) {
		t.Fatalf("first call = %+v", bs[2])
	}
	if bs[3].ID != "fc-2" || bs[3].Signature != "" {
		t.Fatalf("second call = %+v", bs[3])
	}
	ends := 0
	for _, e := range events {
		if e.Type == llm.EventToolUseEnd {
			ends++
		}
	}
	if ends != 2 {
		t.Fatalf("%d tool_use_end events", ends)
	}
	// The request: key in a header, never the URL; system, schema and
	// thinking where the API wants them.
	if s.keys[0] != "secret" || strings.Contains(s.paths[0], "secret") || s.paths[0] != "/v1beta/models/gemini-3-pro:streamGenerateContent?alt=sse" {
		t.Fatalf("path=%s key=%s", s.paths[0], s.keys[0])
	}
	b := s.bodies[0]
	if jsonAt(b, "systemInstruction", "parts", 0, "text") != "be brief" {
		t.Errorf("system = %v", b["systemInstruction"])
	}
	if jsonAt(b, "tools", 0, "functionDeclarations", 0, "parametersJsonSchema", "additionalProperties") != false {
		t.Errorf("tools = %v", b["tools"])
	}
	if jsonAt(b, "generationConfig", "thinkingConfig", "thinkingLevel") != "LOW" || jsonAt(b, "generationConfig", "thinkingConfig", "includeThoughts") != true {
		t.Errorf("generationConfig = %v", b["generationConfig"])
	}
}

// The next request carries the signature back on the call it came with,
// answers each call under its name (and id when the API gave one), and
// does not send the thought summary back.
func TestHistoryEchoesSignaturesAndNamesResults(t *testing.T) {
	p, s := newTest(t, frames(`{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`))
	hist := []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: "hmm"},
			{Type: llm.BlockToolUse, ID: callPrefix + "1", Name: "read_file", Input: json.RawMessage(`{"path":"a"}`), Signature: "c2ln"},
			{Type: llm.BlockToolUse, ID: "fc-2", Name: "glob", Input: json.RawMessage(`{}`)},
		}},
		llm.ToolResultMessage(llm.ToolResult(callPrefix+"1", "package a", false), llm.ToolResult("fc-2", "boom", true)),
	}
	if _, err := p.Stream(context.Background(), llm.Request{Model: "gemini-3-pro", Messages: hist}, nil); err != nil {
		t.Fatal(err)
	}
	c := s.bodies[0]["contents"].([]any)
	if len(c) != 3 {
		t.Fatalf("contents = %v", c)
	}
	model := c[1].(map[string]any)["parts"].([]any)
	if len(model) != 2 {
		t.Fatalf("model parts = %v (thought summary sent back?)", model)
	}
	first := model[0].(map[string]any)
	if first["thoughtSignature"] != "c2ln" || jsonAt(first, "functionCall", "id") != nil {
		t.Errorf("first call part = %v", first)
	}
	if jsonAt(model[1].(map[string]any), "functionCall", "id") != "fc-2" {
		t.Errorf("second call part = %v", model[1])
	}
	res := c[2].(map[string]any)["parts"].([]any)
	if jsonAt(res[0].(map[string]any), "functionResponse", "name") != "read_file" || jsonAt(res[0].(map[string]any), "functionResponse", "response", "result") != "package a" {
		t.Errorf("first result = %v", res[0])
	}
	if jsonAt(res[1].(map[string]any), "functionResponse", "id") != "fc-2" || jsonAt(res[1].(map[string]any), "functionResponse", "response", "error") != "boom" {
		t.Errorf("second result = %v", res[1])
	}
}

// Calls another model made carry no signature; Gemini 3 refuses those, so
// the first call of the step gets Google's stand-in.
func TestUnsignedCallsGetTheStandIn(t *testing.T) {
	c := modelContent(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockText, Text: "calling"},
		{Type: llm.BlockToolUse, ID: "toolu_1", Name: "a", Input: json.RawMessage(`{}`)},
		{Type: llm.BlockToolUse, ID: "toolu_2", Name: "b", Input: json.RawMessage(`{}`)},
	}}, true)
	if c.Parts[1].ThoughtSignature != skipSignature || c.Parts[2].ThoughtSignature != "" {
		t.Fatalf("parts = %+v", c.Parts)
	}
	if c.Parts[1].FunctionCall.ID != "toolu_1" {
		t.Fatalf("id = %q", c.Parts[1].FunctionCall.ID)
	}
}

// A 2.x model refuses thinkingLevel; it is dropped and the request resent
// without spending a retry, and stays dropped.
func TestLearnsToDropThinkingLevel(t *testing.T) {
	refuse := func(w http.ResponseWriter) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"code":400,"message":"Thinking level is not supported for this model.","status":"INVALID_ARGUMENT"}}`)
	}
	ok := frames(`{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`)
	p, s := newTest(t, refuse, ok, ok)
	req := llm.Request{Model: "gemini-2.5-flash", Effort: "high", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
	for i := 0; i < 2; i++ {
		if _, err := p.Stream(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
	}
	if jsonAt(s.bodies[0], "generationConfig", "thinkingConfig", "thinkingLevel") != "HIGH" {
		t.Fatalf("first = %v", s.bodies[0]["generationConfig"])
	}
	for _, b := range s.bodies[1:] {
		if jsonAt(b, "generationConfig", "thinkingConfig", "thinkingLevel") != nil {
			t.Fatalf("level still sent: %v", b["generationConfig"])
		}
	}
}

func TestStreamWithoutFinishIsRetried(t *testing.T) {
	p, s := newTest(t,
		frames(`{"candidates":[{"content":{"parts":[{"text":"hal"}]}}]}`),
		frames(`{"candidates":[{"content":{"parts":[{"text":"whole"}]},"finishReason":"STOP"}]}`),
	)
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, nil)
	if err != nil || resp.Message.Text() != "whole" || len(s.bodies) != 2 {
		t.Fatalf("resp=%v err=%v requests=%d", resp, err, len(s.bodies))
	}
}

func TestBlockedPromptIsARefusal(t *testing.T) {
	p, _ := newTest(t, frames(`{"promptFeedback":{"blockReason":"SAFETY"}}`))
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, nil)
	if err != nil || resp.StopReason != llm.StopRefusal || !strings.Contains(resp.Message.Text(), "declined") {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestNoKeyIsRefused(t *testing.T) {
	if _, err := New(Config{}); err != llm.ErrNoAPIKey {
		t.Fatalf("err = %v", err)
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "secret" {
			w.WriteHeader(403)
			return
		}
		io.WriteString(w, `{"models":[{"name":"models/gemini-3-pro","displayName":"Gemini 3 Pro","inputTokenLimit":1048576,"outputTokenLimit":65536,"supportedGenerationMethods":["generateContent","countTokens"]},{"name":"models/embedding-001","supportedGenerationMethods":["embedContent"]}]}`)
	}))
	defer srv.Close()
	p, _ := New(Config{BaseURL: srv.URL, APIKeys: []string{"secret"}})
	ms, err := p.ListModels(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != "gemini-3-pro" || ms[0].ContextWindow != 1048576 {
		t.Fatalf("models=%+v err=%v", ms, err)
	}
}

// Gemini sends no call ids, so every turn's first call is gemini_call_1,
// and the agent, finding that id taken, renames the later call and
// replies under the original. Results were named by looking the id up in
// one map of the whole history, so each turn's result took the name of
// whichever gemini_call_1 the map held: a bash result went back as the
// answer to read_file.
func TestRepeatedCallIDsNameEachResultByItsOwnCall(t *testing.T) {
	hist := []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: callPrefix + "1", Name: "read_file", Input: json.RawMessage(`{}`)},
		}},
		llm.ToolResultMessage(llm.ToolResult(callPrefix+"1", "package a", false)),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: callPrefix + "1#2", ReplyID: callPrefix + "1", Name: "bash", Input: json.RawMessage(`{}`)},
		}},
		llm.ToolResultMessage(llm.ToolResult(callPrefix+"1", "ok", false)),
	}
	c := contents(hist, false)
	if len(c) != 5 {
		t.Fatalf("contents = %+v", c)
	}
	for i, want := range map[int]string{2: "read_file", 4: "bash"} {
		if got := c[i].Parts[0].FunctionResponse.Name; got != want {
			t.Errorf("result %d is named %q, want %q", i, got, want)
		}
	}
}

// jsonAt walks decoded JSON by keys and indexes; nil when absent.
func jsonAt(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}
