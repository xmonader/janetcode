package llm

import (
	"encoding/json"
	"testing"
)

func TestMessageTextAndToolUses(t *testing.T) {
	m := Message{Role: RoleAssistant, Blocks: []Block{
		{Type: BlockThinking, Text: "hmm"},
		{Type: BlockText, Text: "hello "},
		{Type: BlockToolUse, ID: "t1", Name: "read_file", Input: json.RawMessage(`{"path":"a"}`)},
		{Type: BlockText, Text: "world"},
	}}
	if got := m.Text(); got != "hello world" {
		t.Fatalf("Text() = %q", got)
	}
	uses := m.ToolUses()
	if len(uses) != 1 || uses[0].ID != "t1" || uses[0].Name != "read_file" {
		t.Fatalf("ToolUses() = %+v", uses)
	}
}

func TestRequestValidate(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		ok   bool
	}{
		{"empty", Request{}, false},
		{"no messages", Request{Model: "m"}, false},
		{"assistant first", Request{Model: "m", Messages: []Message{TextMessage(RoleAssistant, "x")}}, false},
		{"empty blocks", Request{Model: "m", Messages: []Message{{Role: RoleUser}}}, false},
		{"ok", Request{Model: "m", Messages: []Message{TextMessage(RoleUser, "x")}}, true},
	}
	for _, c := range cases {
		err := c.req.Validate()
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v ok=%v", c.name, err, c.ok)
		}
	}
}

func TestUsageAdd(t *testing.T) {
	var u Usage
	u.Add(Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4})
	u.Add(Usage{InputTokens: 1, OutputTokens: 2, CacheReadTokens: 3, CacheWriteTokens: 4})
	if u != (Usage{2, 4, 6, 8}) {
		t.Fatalf("usage = %+v", u)
	}
}
