package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// OpenRouter sends the same reasoning text twice in one delta: once as
// "reasoning" and once inside "reasoning_details". Both were written to
// the thinking buffer and emitted, so every snippet showed up doubled in
// stream-json and in the recorded thinking.
func TestReasoningCarriedTwiceIsEmittedOnce(t *testing.T) {
	var got []string
	s := newStreamState(func(e llm.Event) {
		if e.Type == llm.EventThinkingDelta {
			got = append(got, e.Text)
		}
	})
	for _, raw := range []string{
		`{"choices":[{"delta":{"reasoning":"The","reasoning_details":[{"type":"reasoning.text","text":"The"}]}}]}`,
		`{"choices":[{"delta":{"reasoning":" file","reasoning_details":[{"type":"reasoning.text","text":" file"}]}}]}`,
		// A provider that only fills reasoning_details still gets through.
		`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.summary","summary":" exists"}]}}]}`,
		// Both native fields set: the first one wins, not their concatenation.
		`{"choices":[{"delta":{"reasoning_content":".","reasoning":"."}}]}`,
	} {
		var ch wireChunk
		if err := json.Unmarshal([]byte(raw), &ch); err != nil {
			t.Fatal(err)
		}
		s.add(ch)
	}
	if want := []string{"The", " file", " exists", "."}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("thinking deltas = %q, want %q", got, want)
	}
	if s.thinking.String() != "The file exists." {
		t.Fatalf("thinking = %q", s.thinking.String())
	}
}

// Kimi on ai02 sends its reasoning in "reasoning". It was never sent back,
// so the reply after one that reasoned out a design wrote the code with
// none of it (effbench syn-kimi-final lean a-catalog-paging-s1: the plan
// said a descending cursor compares the key reversed, the next reply's
// search() compared it ascending, a hidden-test FAIL). pi sends it back
// in the field it came in.
func TestReasoningIsSentBackInItsOwnField(t *testing.T) {
	for _, field := range []string{"reasoning", "reasoning_content"} {
		s := newStreamState(func(llm.Event) {})
		var ch wireChunk
		if err := json.Unmarshal([]byte(`{"choices":[{"delta":{"`+field+`":"descending: key reversed, id ascending"}}]}`), &ch); err != nil {
			t.Fatal(err)
		}
		s.add(ch)
		resp, err := s.response()
		if err != nil {
			t.Fatal(err)
		}
		if b := resp.Message.Blocks[0]; b.Via != field || !b.Replayed() {
			t.Fatalf("%s: thinking block = %+v, want Via %q", field, b, field)
		}
		reply := resp.Message
		reply.Blocks = append(reply.Blocks, llm.Block{Type: llm.BlockToolUse, ID: "c1", Name: "bash", Input: json.RawMessage(`{}`)})
		req := llm.Request{Model: "m", Messages: []llm.Message{
			llm.TextMessage(llm.RoleUser, "go"),
			reply,
			llm.ToolResultMessage(llm.Block{Type: llm.BlockToolResult, ToolUseID: "c1", Text: "ok"}),
		}}
		for _, drop := range []bool{false, true} {
			req.DropReasoning = drop
			w, err := buildRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(w.Messages[1])
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			got, _ := m[field].(string)
			want := "descending: key reversed, id ascending"
			if drop {
				want = ""
			}
			if got != want {
				t.Fatalf("%s drop=%v: assistant message %s", field, drop, raw)
			}
			other := map[string]string{"reasoning": "reasoning_content", "reasoning_content": "reasoning"}[field]
			if _, ok := m[other]; ok {
				t.Fatalf("%s: also sent as %s: %s", field, other, raw)
			}
		}
	}
}

// Reasoning written inline in the reply text (<think> tags) came from no
// field a server takes back, so it is not sent back.
func TestInlineReasoningIsNotSentBack(t *testing.T) {
	s := newStreamState(func(llm.Event) {})
	var ch wireChunk
	if err := json.Unmarshal([]byte(`{"choices":[{"delta":{"content":"<think>plan</think>done"}}]}`), &ch); err != nil {
		t.Fatal(err)
	}
	s.add(ch)
	resp, err := s.response()
	if err != nil {
		t.Fatal(err)
	}
	w, err := buildRequest(llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "go"), resp.Message}})
	if err != nil {
		t.Fatal(err)
	}
	if am := w.Messages[1]; am.Reasoning != "" || am.ReasoningContent != "" {
		t.Fatalf("inline reasoning sent back: %+v", am)
	}
}
