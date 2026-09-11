package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func args(t *testing.T, c RecoveredCall) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Input, &m); err != nil {
		t.Fatalf("input %s: %v", c.Input, err)
	}
	return m
}

func TestRecoverInvokeBlock(t *testing.T) {
	text := "<function_calls>\n<invoke name=\"read_file\">\n<parameter name=\"path\">main.go</parameter>\n</invoke>\n</function_calls>"
	calls, remaining, ok := RecoverToolCalls(text)
	if !ok || len(calls) != 1 {
		t.Fatalf("ok=%v calls=%d", ok, len(calls))
	}
	if calls[0].Name != "read_file" {
		t.Fatalf("name = %q", calls[0].Name)
	}
	if got := args(t, calls[0])["path"]; got != "main.go" {
		t.Fatalf("path = %v", got)
	}
	if remaining != "" {
		t.Fatalf("remaining = %q", remaining)
	}
}

func TestRecoverKeepsSignificantWhitespaceAndEntities(t *testing.T) {
	text := "<invoke name=\"edit_file\">\n<parameter name=\"old_string\">\n    if a &lt; b {\n</parameter>\n<parameter name=\"path\">a.go</parameter>\n</invoke>"
	calls, _, ok := RecoverToolCalls(text)
	if !ok {
		t.Fatal("not recovered")
	}
	got := args(t, calls[0])["old_string"]
	if got != "    if a < b {" {
		t.Fatalf("old_string = %q", got)
	}
}

func TestRecoverParsesStructuredParameterOnly(t *testing.T) {
	text := "<invoke name=\"todo\">\n<parameter name=\"todos\">[{\"text\":\"a\",\"status\":\"pending\"}]</parameter>\n<parameter name=\"note\">8080</parameter>\n</invoke>"
	calls, _, ok := RecoverToolCalls(text)
	if !ok {
		t.Fatal("not recovered")
	}
	a := args(t, calls[0])
	if _, isList := a["todos"].([]any); !isList {
		t.Fatalf("todos = %#v, want a list", a["todos"])
	}
	if a["note"] != "8080" {
		t.Fatalf("note = %#v, want the string", a["note"])
	}
}

func TestRecoverToolCallJSONBlock(t *testing.T) {
	text := "<tool_call>\n{\"name\": \"bash\", \"arguments\": {\"command\": \"go test ./...\"}}\n</tool_call>"
	calls, _, ok := RecoverToolCalls(text)
	if !ok || len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("ok=%v calls=%+v", ok, calls)
	}
	if got := args(t, calls[0])["command"]; got != "go test ./..." {
		t.Fatalf("command = %v", got)
	}
}

func TestRecoverToolCallDoubleEncodedArguments(t *testing.T) {
	text := `<tool_call>{"name":"grep","arguments":"{\"pattern\":\"func \"}"}</tool_call>`
	calls, _, ok := RecoverToolCalls(text)
	if !ok {
		t.Fatal("not recovered")
	}
	if got := args(t, calls[0])["pattern"]; got != "func " {
		t.Fatalf("pattern = %v", got)
	}
}

func TestRecoverOrdersCallsAsWritten(t *testing.T) {
	text := "<tool_call>{\"name\":\"a\",\"arguments\":{}}</tool_call>\n<invoke name=\"b\">\n<parameter name=\"x\">1</parameter>\n</invoke>\n<tool_call>{\"name\":\"c\",\"arguments\":{}}</tool_call>"
	calls, _, ok := RecoverToolCalls(text)
	if !ok || len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Name != "a" || calls[1].Name != "b" || calls[2].Name != "c" {
		t.Fatalf("order = %v %v %v", calls[0].Name, calls[1].Name, calls[2].Name)
	}
}

func TestRecoverSkipsFencedAndProse(t *testing.T) {
	fenced := "Here is how it looks:\n\n```\n<invoke name=\"read_file\">\n<parameter name=\"path\">a</parameter>\n</invoke>\n```\n"
	if _, _, ok := RecoverToolCalls(fenced); ok {
		t.Fatal("a fenced example is documentation, not a call")
	}
	prose := strings.Repeat("The format the model would use is described below at some length. ", 20) +
		"<invoke name=\"read_file\">\n<parameter name=\"path\">a</parameter>\n</invoke>"
	if _, _, ok := RecoverToolCalls(prose); ok {
		t.Fatal("a footnote to a page of prose is not a call")
	}
	if _, _, ok := RecoverToolCalls("just an answer"); ok {
		t.Fatal("plain text recovered something")
	}
}

func TestRecoverIgnoresParameterlessInvoke(t *testing.T) {
	if _, _, ok := RecoverToolCalls("<invoke name=\"read_file\"></invoke>"); ok {
		t.Fatal("nothing can be called from a block with no parameters")
	}
}

func TestRecoverLeavesSurroundingText(t *testing.T) {
	text := "Reading it now.\n<invoke name=\"read_file\">\n<parameter name=\"path\">a.go</parameter>\n</invoke>"
	calls, remaining, ok := RecoverToolCalls(text)
	if !ok || len(calls) != 1 {
		t.Fatalf("ok=%v calls=%d", ok, len(calls))
	}
	if remaining != "Reading it now." {
		t.Fatalf("remaining = %q", remaining)
	}
}

func TestRecoveredBlocksHaveDistinctIDs(t *testing.T) {
	blocks := RecoveredBlocks([]RecoveredCall{{Name: "a"}, {Name: "b"}}, "recovered_1_1")
	if len(blocks) != 2 || blocks[0].ID == blocks[1].ID {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].Type != BlockToolUse {
		t.Fatalf("type = %v", blocks[0].Type)
	}
}
