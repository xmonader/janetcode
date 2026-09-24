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
