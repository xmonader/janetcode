package llm

import (
	"encoding/json"
	"math"
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
	// Provider numbers are user-hosted input; a broken or hostile
	// endpoint can send negative or NaN fields. Accumulated they would
	// corrupt /stats and let a negative running cost sail under the
	// --max-cost ceiling unenforced.
	u.Add(Usage{InputTokens: -50, OutputTokens: -1, CostUSD: -2})
	u.Add(Usage{CostUSD: math.NaN()})
	u.Add(Usage{CostUSD: math.Inf(1)})
	if u != (Usage{InputTokens: 2, OutputTokens: 4, CacheReadTokens: 6, CacheWriteTokens: 8}) {
		t.Fatalf("usage after garbage = %+v", u)
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
	// The fewest decimals that are honest: no trailing zeros below a
	// dollar, two decimals at or above one, and the precise formatter's
	// significant digits below a cent.
	for in, want := range map[float64]string{
		0.0042: "$0.0042", 0.00023: "$0.00023", 0.01: "$0.01",
		0.2: "$0.2", 0.25: "$0.25", 0.123: "$0.123", 0.9999: "$1.00",
		1.5: "$1.50", 3.4: "$3.40",
	} {
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

// A user turn with nothing to send is not a turn. The OpenAI path used
// to drop it silently, leaving the history with two assistant turns in a
// row or an assistant turn last — which the server rejects with an error
// about the history rather than about the turn that broke it.
func TestValidateRejectsAnEmptyUserTurn(t *testing.T) {
	base := func(blocks ...Block) Request {
		return Request{Model: "m", Messages: []Message{
			TextMessage(RoleUser, "hi"),
			TextMessage(RoleAssistant, "ok"),
			{Role: RoleUser, Blocks: blocks},
		}}
	}
	for _, tc := range []struct {
		name   string
		blocks []Block
		ok     bool
	}{
		{"empty text", []Block{{Type: BlockText}}, false},
		{"several empty texts", []Block{{Type: BlockText}, {Type: BlockText}}, false},
		{"an image with no data", []Block{{Type: BlockImage, MediaType: "image/png"}}, false},
		{"text", []Block{{Type: BlockText, Text: "more"}}, true},
		{"an empty tool result still answers a call", []Block{{Type: BlockToolResult, ToolUseID: "c1"}}, true},
		{"an image", []Block{{Type: BlockImage, MediaType: "image/png", Data: "AAAA"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := base(tc.blocks...).Validate()
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	// An assistant turn may be empty: a model that stopped without saying
	// anything is a thing that happens, and each provider has its own
	// placeholder for it.
	empty := Request{Model: "m", Messages: []Message{
		TextMessage(RoleUser, "hi"),
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockText}}},
		TextMessage(RoleUser, "still there?"),
	}}
	if err := empty.Validate(); err != nil {
		t.Fatalf("an empty assistant turn was rejected: %v", err)
	}
}
