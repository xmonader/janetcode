package agent

import (
	"context"
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
	if kinds[0] != EvCompacted || len(a.Messages) != 2 || a.Messages[1].Text() != "answer one" {
		t.Fatalf("kinds=%v messages=%+v", kinds, a.Messages)
	}
	if !strings.Contains(a.Messages[0].Text(), "note A") || !strings.Contains(a.Messages[0].Text(), "q1") {
		t.Fatalf("compacted first message = %q", a.Messages[0].Text())
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
