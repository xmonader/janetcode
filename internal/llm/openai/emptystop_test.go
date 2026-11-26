package openai

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func stream(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// An empty "stop" is a proxy glitch: it must surface as a retryable
// truncated stream, while a stop with text or reasoning is an answer.
func TestEmptyStopIsRetried(t *testing.T) {
	_, err := readStream(strings.NewReader(stream(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)), nil, func(llm.Event) {})
	if err == nil || !llm.Truncated(err) || !llm.Retryable(err) {
		t.Fatalf("empty stop = %v, want a retryable truncated error", err)
	}
	for name, chunk := range map[string]string{
		"text":      `{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
		"reasoning": `{"choices":[{"delta":{"reasoning_content":"hmm"},"finish_reason":"stop"}]}`,
	} {
		if _, err := readStream(strings.NewReader(stream(chunk)), nil, func(llm.Event) {}); err != nil {
			t.Errorf("%s stop failed: %v", name, err)
		}
	}
}
