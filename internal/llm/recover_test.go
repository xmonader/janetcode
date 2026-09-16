package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
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

// Where a block sits relative to the code fences around it decides
// whether it is a call or an example of one. These are the shapes that
// decide it, pinned before and after the scan was made linear.
func TestRecoverToolCallsAndFences(t *testing.T) {
	call := "<invoke name=\"read_file\">\n<parameter name=\"path\">a</parameter>\n</invoke>"
	for _, tc := range []struct {
		name, text string
		want       bool
	}{
		{"bare call", call, true},
		{"inside a fence", "```\n" + call + "\n```", false},
		{"inside a tilde fence", "~~~\n" + call + "\n~~~", false},
		{"after a closed fence", "```\nexample\n```\n" + call, true},
		{"after a fence closed by a longer one", "```\nx\n````\n" + call, true},
		// A fence is closed by its own character, not by the other one.
		{"a tilde does not close a backtick fence", "```\n~~~\n" + call + "\n```", false},
		// An info string on the closing line means it opens, not closes.
		{"a second opener is not a closer", "```go\nx\n```go\n" + call, false},
		{"indented fence still opens", "   ```\n" + call + "\n   ```", false},
		{"four spaces is not a fence", "    ```\n" + call, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, _, ok := RecoverToolCalls(tc.text)
			if ok != tc.want {
				t.Fatalf("recovered %v (%d calls), want %v", ok, len(calls), tc.want)
			}
		})
	}
}

// A reply that is nothing but blocks must not cost time quadratic in its
// length: the fences are found once, not once per block.
func TestRecoverToolCallsManyBlocks(t *testing.T) {
	var sb strings.Builder
	for i := range 400 {
		fmt.Fprintf(&sb, "<invoke name=\"read_file\">\n<parameter name=\"path\">f%d</parameter>\n</invoke>\n", i)
	}
	start := time.Now()
	calls, _, ok := RecoverToolCalls(sb.String())
	if !ok || len(calls) != 400 {
		t.Fatalf("recovered %v, %d calls", ok, len(calls))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("400 blocks took %s", d)
	}
}
