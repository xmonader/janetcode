package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// reasoning_effort goes out only when a caller names it.
func TestReasoningEffortSentOnlyOnRequest(t *testing.T) {
	msgs := []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}
	w, err := buildRequest(llm.Request{Model: "m", Messages: msgs, MaxTokens: 10, Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(w); strings.Contains(string(b), "reasoning_effort") {
		t.Fatalf("Effort alone sent reasoning_effort: %s", b)
	}
	w, _ = buildRequest(llm.Request{Model: "m", Messages: msgs, MaxTokens: 10, ReasoningEffort: "low"})
	if b, _ := json.Marshal(w); !strings.Contains(string(b), `"reasoning_effort":"low"`) {
		t.Fatalf("ReasoningEffort not sent: %s", b)
	}
	p := &Provider{dialect: &dialect{noEffort: true}}
	if got := p.applyDialect(w); got.ReasoningEffort != "" {
		t.Fatal("a server that rejected the field still gets it")
	}
}
