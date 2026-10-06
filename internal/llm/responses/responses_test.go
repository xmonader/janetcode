package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/httpstream"
)

type server struct {
	t      *testing.T
	mu     sync.Mutex
	bodies []map[string]any
	auth   []string
	reply  []func(w http.ResponseWriter)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/responses" {
		s.t.Errorf("path %s", r.URL.Path)
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	i := len(s.bodies)
	s.bodies = append(s.bodies, body)
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	s.mu.Unlock()
	if i >= len(s.reply) {
		s.t.Errorf("unexpected request %d", i+1)
		w.WriteHeader(500)
		return
	}
	s.reply[i](w)
}

// events writes named SSE events, the way OpenAI does.
func events(es ...string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range es {
			var t struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(e), &t)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", t.Type, e)
		}
	}
}

func newTest(t *testing.T, replies ...func(w http.ResponseWriter)) (*Provider, *server) {
	t.Helper()
	s := &server{t: t, reply: replies}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL + "/v1", APIKeys: []string{"sk-test"}, Thoughts: true})
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}

var toolTurn = []string{
	`{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}`,
	`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
	`{"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_1","summary_index":0,"delta":"Need the file."}`,
	`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"Need the file."}],"encrypted_content":"gAAAAenc"}}`,
	`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
	`{"type":"response.output_text.delta","output_index":1,"item_id":"msg_1","content_index":0,"delta":"Reading "}`,
	`{"type":"response.output_text.delta","output_index":1,"item_id":"msg_1","content_index":0,"delta":"it."}`,
	`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_A","name":"read_file","arguments":""}}`,
	`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_1","delta":"{\"path\":"}`,
	`{"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_1","delta":"\"a.go\"}"}`,
	`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_A","name":"read_file","arguments":"{\"path\":\"a.go\"}"}}`,
	`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2-2026","status":"completed","usage":{"input_tokens":500,"input_tokens_details":{"cached_tokens":300},"output_tokens":90,"output_tokens_details":{"reasoning_tokens":64}}}}`,
}

func TestStreamsReasoningTextAndACall(t *testing.T) {
	p, s := newTest(t, events(toolTurn...))
	var kinds []llm.EventType
	resp, err := p.Stream(context.Background(), llm.Request{
		Model: "gpt-5.2", System: "be terse", MaxTokens: 2000, Effort: "xhigh",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "read a.go")},
		Tools:    []llm.ToolDef{{Name: "read_file", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, func(e llm.Event) { kinds = append(kinds, e.Type) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.StopReason != llm.StopToolUse || resp.Model != "gpt-5.2-2026" {
		t.Fatalf("stop=%s model=%s", resp.StopReason, resp.Model)
	}
	if want := (llm.Usage{InputTokens: 200, CacheReadTokens: 300, OutputTokens: 90, ReasoningTokens: 64}); resp.Usage != want {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	bs := resp.Message.Blocks
	if len(bs) != 3 {
		t.Fatalf("blocks = %+v", bs)
	}
	if bs[0].Type != llm.BlockThinking || bs[0].ID != "rs_1" || bs[0].Signature != "gAAAAenc" || bs[0].Text != "Need the file." {
		t.Errorf("reasoning = %+v", bs[0])
	}
	if bs[1].Text != "Reading it." {
		t.Errorf("text = %+v", bs[1])
	}
	if bs[2].ID != "call_A" || bs[2].Name != "read_file" || string(bs[2].Input) != `{"path":"a.go"}` {
		t.Errorf("call = %+v", bs[2])
	}
	b := s.bodies[0]
	if s.auth[0] != "Bearer sk-test" || b["store"] != false || b["instructions"] != "be terse" || b["max_output_tokens"] != float64(2000) {
		t.Errorf("body = %v auth = %s", b, s.auth[0])
	}
	if r, _ := b["reasoning"].(map[string]any); r["effort"] != "xhigh" || r["summary"] != "auto" {
		t.Errorf("reasoning = %v", b["reasoning"])
	}
	if inc, _ := b["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", b["include"])
	}
	if tl := b["tools"].([]any)[0].(map[string]any); tl["type"] != "function" || tl["name"] != "read_file" || tl["strict"] != false {
		t.Errorf("tool = %v", tl)
	}
}

// The reasoning item goes back before the call it led to, carrying its
// encrypted content; the result follows the call as its own item.
func TestHistoryReplaysReasoningInPlace(t *testing.T) {
	p, s := newTest(t, events(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m"}}`, `{"type":"response.output_text.delta","output_index":0,"delta":"ok"}`, `{"type":"response.completed","response":{"status":"completed"}}`))
	hist := []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, ID: "rs_1", Text: "plan", Signature: "enc"},
			{Type: llm.BlockThinking, Text: "anthropic thinking", Signature: "sig-from-elsewhere"},
			{Type: llm.BlockText, Text: "Reading."},
			{Type: llm.BlockToolUse, ID: "h1", ReplyID: "call_A", Name: "read_file", Input: json.RawMessage(`{"path":"a"}`)},
		}},
		llm.ToolResultMessage(llm.ToolResult("h1", "no such file", true)),
	}
	resp, err := p.Stream(context.Background(), llm.Request{Model: "gpt-5.2", Messages: hist}, nil)
	if err != nil || resp.Message.Text() != "ok" || resp.StopReason != llm.StopEndTurn {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	in := s.bodies[0]["input"].([]any)
	types := []string{}
	for _, x := range in {
		m := x.(map[string]any)
		if t, ok := m["type"].(string); ok {
			types = append(types, t)
		} else {
			types = append(types, "msg:"+m["role"].(string))
		}
	}
	if strings.Join(types, ",") != "msg:user,reasoning,msg:assistant,function_call,function_call_output" {
		t.Fatalf("input items = %v", types)
	}
	rs := in[1].(map[string]any)
	if rs["id"] != "rs_1" || rs["encrypted_content"] != "enc" {
		t.Errorf("reasoning = %v", rs)
	}
	if in[3].(map[string]any)["call_id"] != "call_A" {
		t.Errorf("call = %v", in[3])
	}
	out := in[4].(map[string]any)
	if out["call_id"] != "call_A" || out["output"] != "Error: no such file" {
		t.Errorf("output = %v", out)
	}
}

// A model that is not a reasoning model refuses the reasoning parameter;
// it is dropped and the request resent at once.
func TestLearnsToDropReasoning(t *testing.T) {
	refuse := func(w http.ResponseWriter) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"Unsupported parameter: 'reasoning.effort' is not supported with this model.","type":"invalid_request_error","param":"reasoning.effort"}}`)
	}
	ok := events(`{"type":"response.output_text.delta","output_index":0,"delta":"hi"}`, `{"type":"response.completed","response":{"status":"completed"}}`)
	p, s := newTest(t, refuse, ok)
	if _, err := p.Stream(context.Background(), llm.Request{Model: "gpt-4.1", Effort: "high", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil); err != nil {
		t.Fatal(err)
	}
	if s.bodies[1]["reasoning"] != nil || s.bodies[1]["include"] != nil {
		t.Fatalf("resent with reasoning: %v", s.bodies[1])
	}
}

func TestIncompleteIsMaxTokens(t *testing.T) {
	p, _ := newTest(t, events(`{"type":"response.output_text.delta","output_index":0,"delta":"par"}`, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`))
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, nil)
	if err != nil || resp.StopReason != llm.StopMaxTokens || resp.Message.Text() != "par" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestFailedResponseIsAnError(t *testing.T) {
	p, _ := newTest(t, events(`{"type":"response.failed","response":{"status":"failed","error":{"code":"invalid_prompt","message":"prompt flagged"}}}`))
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, nil)
	var ae *httpstream.APIError
	if !errors.As(err, &ae) || !strings.Contains(ae.Message, "prompt flagged") || llm.Retryable(err) {
		t.Fatalf("err = %v", err)
	}
	if !llm.Retryable(streamError(&apiError{Code: "rate_limit_exceeded", Message: "slow down"})) {
		t.Fatal("a mid-stream rate limit is not retryable")
	}
}

func TestStreamCutOffIsRetried(t *testing.T) {
	p, s := newTest(t,
		events(`{"type":"response.output_text.delta","output_index":0,"delta":"hal"}`),
		events(`{"type":"response.output_text.delta","output_index":0,"delta":"whole"}`, `{"type":"response.completed","response":{"status":"completed"}}`),
	)
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "x")}}, nil)
	if err != nil || resp.Message.Text() != "whole" || len(s.bodies) != 2 {
		t.Fatalf("resp=%v err=%v requests=%d", resp, err, len(s.bodies))
	}
}
