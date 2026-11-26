package openai

import (
	"encoding/json"
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
