package eval

import "testing"

// TestOpenclawReplyIsExtracted: openclaw returns the reply in a named
// field beside a large diagnostic block. Without pulling it out, the whole
// object is handed to a check as "the model's answer" and a task judged on
// the final message fails on a harness that answered correctly — which is
// what happened to 06-read-and-answer, three times out of three.
func TestOpenclawReplyIsExtracted(t *testing.T) {
	out := []byte(`{"payloads":[{"text":"21","mediaUrl":null}],` +
		`"meta":{"durationMs":4275,"finalAssistantVisibleText":"21","stopReason":"stop"}}`)
	if got := ExtractAssistantText(out); got != "21" {
		t.Fatalf("got %q, want %q", got, "21")
	}
	// The payload alone is enough when the meta field is absent.
	if got := ExtractAssistantText([]byte(`{"payloads":[{"text":"hello"}]}`)); got != "hello" {
		t.Fatalf("got %q", got)
	}
	// Anything that is not an openclaw result is left to the other paths.
	if got := openclawText([]byte(`{"result":"other harness"}`)); got != "" {
		t.Fatalf("should not claim a foreign object: %q", got)
	}
}

// TestOpenclawUsageIsReadFromItsUsageBlock: the same object carries
// promptTokens (the size of the last prompt, cache included) next to the
// usage block. The generic walker added it to usage.input and reported
// 56509 where openclaw itself said 20200. costUsd of 0 is a provider with
// no price table, not a free run.
func TestOpenclawUsageIsReadFromItsUsageBlock(t *testing.T) {
	out := []byte(`{"payloads":[{"text":"15"}],"meta":{"agentMeta":{` +
		`"usage":{"input":20200,"output":378,"cacheRead":87168,"reasoningTokens":173,"total":107746},` +
		`"lastCallUsage":{"input":2261,"output":91,"cacheRead":34048,"cacheWrite":0},` +
		`"promptTokens":36309,"assistantTurns":3,"costUsd":0},` +
		`"toolSummary":{"calls":4,"failures":0},"finalAssistantVisibleText":"15"}}`)
	u := ParseUsageJSON(out)
	if u.Input.Value != 20200 || u.Output.Value != 378 || u.CacheRead.Value != 87168 {
		t.Fatalf("usage = %+v", u)
	}
	if u.CacheWrite.Set {
		t.Fatalf("cacheWrite was not reported in the usage block: %+v", u.CacheWrite)
	}
	if u.Turns.Value != 3 || u.ToolCalls.Value != 4 {
		t.Fatalf("turns/tool calls = %+v / %+v", u.Turns, u.ToolCalls)
	}
	if u.CostUSD.Set {
		t.Fatalf("a zero cost is unknown, not reported: %+v", u.CostUSD)
	}
	if _, ok := openclawUsage([]byte(`{"usage":{"input":1,"output":1}}`)); ok {
		t.Fatal("a foreign usage object was claimed as openclaw's")
	}
}
