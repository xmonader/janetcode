package llm

import (
	"encoding/json"
	"strings"
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
	if u != (Usage{InputTokens: 2, OutputTokens: 4, CacheReadTokens: 6, CacheWriteTokens: 8}) {
		t.Fatalf("usage = %+v", u)
	}
}

func TestUsageCost(t *testing.T) {
	u := Usage{InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 500_000}
	if _, ok := u.Cost(nil); ok {
		t.Fatal("no price and no reported cost must be unknown")
	}
	usd, ok := u.Cost(&Price{Input: 3, Output: 15, CacheRead: 0.3})
	if !ok || usd < 4.649 || usd > 4.651 {
		t.Fatalf("estimate = %v %v", usd, ok)
	}
	u.CostUSD = 0.5
	if usd, ok := u.Cost(&Price{Input: 3}); !ok || usd != 0.5 {
		t.Fatalf("reported cost must win: %v %v", usd, ok)
	}
	var sum Usage
	sum.Add(u)
	sum.Add(u)
	if sum.CostUSD != 1.0 {
		t.Fatalf("Add cost = %v", sum.CostUSD)
	}
	for in, want := range map[float64]string{0.0042: "$0.0042", 0.123: "$0.123", 3.4: "$3.40"} {
		if got := FormatUSD(in); got != want {
			t.Fatalf("FormatUSD(%v) = %q", in, got)
		}
	}
}

// TestHarnessMessageIsMarkedSynthetic: text the harness writes into the
// user role carries the mark, survives a session round trip, and a plain
// user message never carries it.
func TestHarnessMessageIsMarkedSynthetic(t *testing.T) {
	m := HarnessMessage(RoleUser, "The check command has not passed yet.")
	if len(m.Blocks) != 1 || !m.Blocks[0].Synthetic || m.Blocks[0].Type != BlockText {
		t.Fatalf("harness message = %+v", m)
	}
	if TextMessage(RoleUser, "hi").Blocks[0].Synthetic {
		t.Fatal("a user message must not be synthetic")
	}
	b, _ := json.Marshal(m)
	var back Message
	if err := json.Unmarshal(b, &back); err != nil || !back.Blocks[0].Synthetic {
		t.Fatalf("round trip lost the mark: %s %v", b, err)
	}
	if b, _ := json.Marshal(TextMessage(RoleUser, "hi")); strings.Contains(string(b), "synthetic") {
		t.Fatalf("plain message serialises the mark: %s", b)
	}
}

// A cheap model's figures live below the point where the compact money
// format stops: four decimals turn a whole task's cost into "$0.0002" and
// a small ceiling into "$0.0000", which reports nothing at all.
func TestFormatUSDPreciseKeepsSubCentFigures(t *testing.T) {
	cases := map[float64]string{
		0.00023:     "$0.00023",
		0.000004567: "$0.00000457",
		0.0042:      "$0.0042",
		0.0001:      "$0.0001",
		0:           "$0.00",
		0.123:       "$0.123",
		3.4:         "$3.40",
	}
	for in, want := range cases {
		if got := FormatUSDPrecise(in); got != want {
			t.Errorf("FormatUSDPrecise(%v) = %q, want %q", in, got, want)
		}
	}
	// Two ceilings that the compact format cannot tell apart must still
	// read as two different numbers.
	if a, b := FormatUSDPrecise(0.00005), FormatUSDPrecise(0.00009); a == b {
		t.Errorf("%s and %s are the same figure", a, b)
	}
}
