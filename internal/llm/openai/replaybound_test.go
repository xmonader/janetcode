package openai

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

// replayReq is a conversation of n assistant exchanges, each with a
// reasoning block of 40 filler characters ending in "END<letter>".
func replayReq(n int) llm.Request {
	req := llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "go")}}
	for i := 0; i < n; i++ {
		id := string(rune('a' + i))
		req.Messages = append(req.Messages,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockThinking, Via: "reasoning", Text: strings.Repeat("x", 40) + "END" + id},
				{Type: llm.BlockToolUse, ID: "c" + id, Name: "bash", Input: json.RawMessage(`{}`)},
			}},
			llm.ToolResultMessage(llm.Block{Type: llm.BlockToolResult, ToolUseID: "c" + id, Text: "ok"}))
	}
	return req
}

func wireReasoning(t *testing.T, req llm.Request) []string {
	t.Helper()
	w, err := buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range w.Messages {
		if m.Role == "assistant" {
			out = append(out, m.Reasoning)
		}
	}
	return out
}

func TestReasoningCharsKeepsTail(t *testing.T) {
	req := replayReq(1)
	req.ReasoningChars = 6
	if got := wireReasoning(t, req); got[0] != "…xxENDa" {
		t.Fatalf("got %q", got[0])
	}
	req.ReasoningChars = 1000
	if got := wireReasoning(t, req); got[0] != strings.Repeat("x", 40)+"ENDa" {
		t.Fatalf("under the cap must be untouched, got %q", got[0])
	}
}

func TestReasoningLastReplaysOnlyTheTail(t *testing.T) {
	req := replayReq(4)
	req.ReasoningLast = 2
	got := wireReasoning(t, req)
	if got[0] != "" || got[1] != "" || got[2] == "" || got[3] == "" {
		t.Fatalf("got %q", got)
	}
}

func TestReasoningKnobsZeroIsAsBefore(t *testing.T) {
	req := replayReq(3)
	for _, r := range wireReasoning(t, req) {
		if !strings.HasPrefix(r, "xxxx") || len(r) != 44 {
			t.Fatalf("unbounded replay changed: %q", r)
		}
	}
}

func TestDropReasoningBeatsTheKnobs(t *testing.T) {
	req := replayReq(3)
	req.DropReasoning, req.ReasoningChars, req.ReasoningLast = true, 5, 2
	for _, r := range wireReasoning(t, req) {
		if r != "" {
			t.Fatalf("reasoning sent despite DropReasoning: %q", r)
		}
	}
}

// A later request that only appends must render the shared prefix
// byte for byte, or the provider's cache is lost.
func TestReasoningCharsIsPrefixStable(t *testing.T) {
	short, long := replayReq(3), replayReq(4)
	short.ReasoningChars, long.ReasoningChars = 10, 10
	ws, err := buildRequest(short)
	if err != nil {
		t.Fatal(err)
	}
	wl, err := buildRequest(long)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range ws.Messages {
		a, _ := json.Marshal(m)
		b, _ := json.Marshal(wl.Messages[i])
		if string(a) != string(b) {
			t.Fatalf("message %d changed:\n%s\n%s", i, a, b)
		}
	}
}

func TestDropReasoningOmitsIt(t *testing.T) {
	req := replayReq(1)
	if got := wireReasoning(t, req); got[0] == "" {
		t.Fatal("replay requested but nothing sent")
	}
	req.DropReasoning = true
	if got := wireReasoning(t, req); got[0] != "" {
		t.Fatalf("dropped reasoning was sent: %q", got[0])
	}
}

// A provider that 400s on a tool-call turn without reasoning_content gets
// replay switched on and the request resent exactly once; later requests
// keep replaying without another round trip.
func TestRequiredReasoningTurnsReplayOn(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		if !strings.Contains(string(raw), "ENDa") {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"The reasoning_content in the thinking mode must be passed back to the API."}}`)
			return
		}
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	p, err := New(Config{BaseURL: srv.URL + "/v1/", APIKey: "k", MaxRetries: 2})
	if err != nil {
		t.Fatal(err)
	}
	req := replayReq(1)
	req.MaxTokens = 50
	req.DropReasoning = true
	for i := 0; i < 2; i++ {
		bodies = nil
		resp, err := p.Stream(context.Background(), req, nil)
		if err != nil || resp.Message.Text() != "ok" {
			t.Fatalf("err=%v resp=%+v", err, resp)
		}
		want := 1
		if i == 0 {
			want = 2
		}
		if len(bodies) != want {
			t.Fatalf("call %d: %d attempts, want %d", i, len(bodies), want)
		}
	}
}

func TestReasoningRequiredMatch(t *testing.T) {
	for msg, want := range map[string]bool{
		"missing reasoning_content in assistant message": true,
		"reasoning is required":                          true,
		"reasoning_effort must be low, medium or high":   false,
		"max_tokens must be positive":                    false,
	} {
		if reasoningRequired(msg) != want {
			t.Errorf("%q: want %v", msg, want)
		}
	}
}
