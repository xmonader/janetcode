package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestCompactKeepsPendingUserText(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("SUMMARY: did things")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "first"),
		llm.TextMessage(llm.RoleAssistant, "ok"),
		llm.TextMessage(llm.RoleUser, "second request"),
	}}
	var evs []Event
	if err := a.Compact(context.Background(), func(e Event) { evs = append(evs, e) }); err != nil {
		t.Fatal(err)
	}
	req := f.requests[0]
	if req.System != compactSystem || len(req.Tools) != 0 || len(req.Messages) != 3 || req.Messages[2].Text() != "Write the handoff note now." {
		t.Fatalf("compaction request = %+v", req)
	}
	if len(a.Messages) != 1 || a.Messages[0].Role != llm.RoleUser || len(a.Messages[0].Blocks) != 2 {
		t.Fatalf("messages = %+v", a.Messages)
	}
	if !strings.Contains(a.Messages[0].Blocks[0].Text, "SUMMARY: did things") || a.Messages[0].Blocks[1].Text != "second request" {
		t.Fatalf("blocks = %+v", a.Messages[0].Blocks)
	}
	if len(evs) != 1 || evs[0].Kind != EvCompacted {
		t.Fatalf("events = %+v", evs)
	}
	if err := (&Agent{Provider: f, Tools: tool.NewRegistry()}).Compact(context.Background(), nil); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err = %v", err)
	}
}

func TestAutoCompactBeforeCallAndOnOverflow(t *testing.T) {
	big := strings.Repeat("x", 4000) // ~1000 tokens
	f := &fake{responses: []*llm.Response{
		text("note A"),     // compaction triggered by estimate
		text("answer one"), // real call
		nil,                // overflow error (errs below)
		text("note B"),     // compaction triggered by overflow
		text("answer two"), // retry
	}, errs: []error{nil, nil, errors.New("400: prompt is too long: 210000 tokens > 200000 maximum")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", CompactAt: 500}
	a.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, big), llm.TextMessage(llm.RoleAssistant, "ok")}
	var kinds []EventKind
	emit := func(e Event) { kinds = append(kinds, e.Kind) }
	if err := a.Run(context.Background(), "q1", emit); err != nil {
		t.Fatal(err)
	}
	// note, kept tail ("ok"), q1, answer one.
	if kinds[0] != EvCompacted || len(a.Messages) != 4 || a.Messages[1].Text() != "ok" || a.Messages[3].Text() != "answer one" {
		t.Fatalf("kinds=%v messages=%+v", kinds, a.Messages)
	}
	if !strings.Contains(a.Messages[0].Text(), "note A") || a.Messages[2].Text() != "q1" {
		t.Fatalf("compacted messages = %+v", a.Messages)
	}
	a.CompactAt = 1_000_000 // estimate says fine; the provider will disagree
	kinds = nil
	if err := a.Run(context.Background(), "q2", emit); err != nil {
		t.Fatal(err)
	}
	if kinds[0] != EvCompacted || a.Messages[len(a.Messages)-1].Text() != "answer two" || len(f.requests) != 5 {
		t.Fatalf("kinds=%v requests=%d last=%q", kinds, len(f.requests), a.Messages[len(a.Messages)-1].Text())
	}
	if !strings.Contains(a.Messages[0].Text(), "note B") {
		t.Fatalf("second compaction missing: %q", a.Messages[0].Text())
	}
}

func TestCompactMidTurnMarksContinuation(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("note")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "do it"),
		toolCall("t1", "read_file", `{}`).Message,
		llm.ToolResultMessage(llm.ToolResult("t1", "big output", false)),
	}}
	if err := a.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Messages[0].Text(), "interrupted mid-task") {
		t.Fatalf("missing continuation marker: %q", a.Messages[0].Text())
	}
}

func TestEstimateTokensUsesRealUsage(t *testing.T) {
	f := &fake{responses: []*llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "a"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 9000, CacheReadTokens: 1000}}}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", CompactAt: -1}
	if err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if est := a.EstimateTokens(); est < 10000 || est > 10100 {
		t.Fatalf("estimate = %d", est)
	}
	if !isContextOverflow(errors.New("context_length_exceeded")) || isContextOverflow(errors.New("rate limited")) {
		t.Fatal("overflow detection wrong")
	}
}

func TestPruneDedupesReadsAndTrimsOldOutput(t *testing.T) {
	root := t.TempDir()
	reg := tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root})
	long := strings.Repeat("y", 10_000)
	use := func(id, name, input string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}}
	}
	res := func(id, text string) llm.Message { return llm.ToolResultMessage(llm.ToolResult(id, text, false)) }
	a := &Agent{Provider: &fake{}, Tools: reg, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		use("1", "read_file", `{"path":"a.go"}`), res("1", "first read "+long),
		use("2", "write_file", `{"path":"b.go","content":"x"}`), res("2", "wrote "+long),
		use("3", "read_file", `{ "path" : "a.go" }`), res("3", "second read "+long),
		use("4", "read_file", `{"path":"c.go"}`), res("4", "c "+long),
		llm.TextMessage(llm.RoleAssistant, "done"),
		llm.TextMessage(llm.RoleUser, "next"),
	}}
	before := a.EstimateTokens()
	if saved := a.Prune(); saved < 17_000 {
		t.Fatalf("saved %d bytes", saved)
	}
	get := func(i int) string { return a.Messages[i].Blocks[0].Text }
	if get(2) != superseded {
		t.Fatalf("first read should be superseded: %q", get(2)[:40])
	}
	if !strings.HasPrefix(get(4), "wrote ") || len(get(4)) > pruneKeep+100 || !strings.Contains(get(4), "omitted") {
		t.Fatalf("old write result should be trimmed: len=%d", len(get(4)))
	}
	// The recent tail (the last six messages, from an assistant call on)
	// is untouched, including the newest read of a.go.
	if len(get(6)) != len("second read "+long) || len(get(8)) != len("c "+long) {
		t.Fatalf("tail results were modified: %d %d", len(get(6)), len(get(8)))
	}
	if a.EstimateTokens() >= before {
		t.Fatal("estimate did not drop")
	}
}

func TestCompactKeepsRecentTail(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("SUMMARY")}}
	big := strings.Repeat("z", 8000)
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", CompactAt: 100, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, big),
		llm.TextMessage(llm.RoleAssistant, "old answer"),
		llm.TextMessage(llm.RoleUser, "second question"),
		llm.TextMessage(llm.RoleAssistant, "recent answer"),
		llm.TextMessage(llm.RoleUser, "pending"),
	}}
	if err := a.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// note, the recent tail from the earliest assistant message that fits,
	// then the pending user text.
	if len(a.Messages) != 5 || !strings.Contains(a.Messages[0].Text(), "SUMMARY") || a.Messages[1].Text() != "old answer" || a.Messages[3].Text() != "recent answer" || a.Messages[4].Text() != "pending" {
		t.Fatalf("messages = %+v", a.Messages)
	}
	// The summariser saw only the head.
	if got := len(f.requests[0].Messages); got != 2 { // big + instruction
		t.Fatalf("summariser got %d messages", got)
	}
}

func TestOverflowLowersCompactThreshold(t *testing.T) {
	for msg, want := range map[string]int{
		"400: prompt is too long: 210000 tokens > 200000 maximum":                           200000,
		"This model's maximum context length is 32768 tokens. However, you requested 40000": 32768,
		"rate limited": 0,
	} {
		if got := windowFromError(errors.New(msg)); got != want {
			t.Errorf("%q -> %d, want %d", msg, got, want)
		}
	}
	f := &fake{responses: []*llm.Response{nil, text("note"), text("ok")},
		errs: []error{errors.New("maximum context length is 32768 tokens")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("x", 4000)), llm.TextMessage(llm.RoleAssistant, "old")}}
	if err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatal(err)
	}
	if a.CompactAt != 32768*3/4 || a.CompactThreshold() != 24576 {
		t.Fatalf("CompactAt = %d", a.CompactAt)
	}
}
