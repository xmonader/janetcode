package eval

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
)

func janetEnvelope(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(headless.CLIResult{
		Type: headless.CLIResultType, Harness: "janetcode",
		Result: "the answer", SessionID: "s1", Model: "m", Provider: "openai",
		Usage:      headless.CLIUsage{InputTokens: 1200, OutputTokens: 300, CacheReadTokens: 9000, CacheWriteTokens: 40},
		CostUSD:    0.0123,
		CostSource: "list price",
		NumTurns:   4, ToolCalls: 7, PermissionDenials: 1,
		Verdict: "tests pass", DurationMS: 4321,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJanetEnvelopeIsReadNatively(t *testing.T) {
	out := janetEnvelope(t)
	u := ParseUsageJSON(out)
	if u.Input.Value != 1200 || u.Output.Value != 300 || u.CacheRead.Value != 9000 || u.CacheWrite.Value != 40 {
		t.Fatalf("usage = %+v", u)
	}
	if u.Turns.Value != 4 || u.ToolCalls.Value != 7 {
		t.Fatalf("turns=%v tools=%v", u.Turns, u.ToolCalls)
	}
	if !u.CostUSD.Set || u.CostUSD.Value != 0.0123 {
		t.Fatalf("cost = %+v", u.CostUSD)
	}
	if got := ExtractAssistantText(out); got != "the answer" {
		t.Fatalf("text = %q", got)
	}
}

// stream-json ends with the same object, so the same reader works.
func TestJanetEnvelopeAtTheEndOfAStream(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"text","text":"the "}`,
		`{"type":"tool_start","name":"bash"}`,
		string(janetEnvelope(t)),
	}, "\n")
	if got := ExtractAssistantText([]byte(stream)); got != "the answer" {
		t.Fatalf("text = %q", got)
	}
	if u := ParseUsageJSON([]byte(stream)); u.Input.Value != 1200 || u.ToolCalls.Value != 7 {
		t.Fatalf("usage = %+v", u)
	}
}

// Another harness's object must not be mistaken for janetcode's.
func TestForeignObjectIsNotJanetcode(t *testing.T) {
	for _, s := range []string{
		`{"type":"result","result":"x","usage":{"input_tokens":5}}`,
		`{"harness":"qwencode","type":"result"}`,
		`not json at all`,
	} {
		if _, ok := parseJanetResult([]byte(s)); ok {
			t.Errorf("%q was read as janetcode's envelope", s)
		}
	}
}
