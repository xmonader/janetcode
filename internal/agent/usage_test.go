package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// A session's totals cannot say which model spent them: it routes cheap
// work elsewhere, falls back when a call fails, asks an advisor, and
// delegates to subagents that may run on a different model again.
func TestUsageIsSplitByTheModelThatServedTheCall(t *testing.T) {
	a := &Agent{}
	a.UpdateSettings(func(s *Settings) { s.Model = "big" })
	a.addUsage("big", llm.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 900})
	a.addUsage("big", llm.Usage{InputTokens: 50, OutputTokens: 5})
	a.addUsage("small", llm.Usage{InputTokens: 10, OutputTokens: 1})
	a.addSideUsage("subagent-model", llm.Usage{InputTokens: 7, OutputTokens: 2})
	// A call whose model is unknown is attributed to the session's.
	a.addUsage("", llm.Usage{InputTokens: 1, OutputTokens: 1})

	rows := a.UsageByModel()
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Model != "big" || rows[0].Calls != 3 || rows[0].Usage.InputTokens != 151 || rows[0].Usage.CacheReadTokens != 900 {
		t.Fatalf("busiest row = %+v", rows[0])
	}
	got := map[string]ModelUsage{}
	for _, r := range rows {
		got[r.Model] = r
	}
	if r := got["subagent-model"]; r.Calls != 1 || r.Usage.OutputTokens != 2 {
		t.Fatalf("subagent row = %+v", r)
	}
	if r := got["small"]; r.Calls != 1 || r.Usage.InputTokens != 10 {
		t.Fatalf("routed row = %+v", r)
	}
	// The split adds up to the session total.
	total := a.UsageSnapshot()
	var sum llm.Usage
	for _, r := range rows {
		sum.Add(r.Usage)
	}
	if sum != total {
		t.Fatalf("the per-model split %+v does not add up to %+v", sum, total)
	}
	out := FormatUsageByModel(rows, a.PriceOf)
	if !strings.Contains(out, "per model") || !strings.Contains(out, "big ") || !strings.Contains(out, "3 calls") {
		t.Fatalf("rendered:\n%s", out)
	}
	// Each row says how much of its input the cache served — 900 of the
	// 1051 prompt tokens on "big" — and a model that cached nothing says
	// nothing rather than "0%".
	if !strings.Contains(out, "cache hit 85%") {
		t.Fatalf("no cache rate on the busiest row:\n%s", out)
	}
	if strings.Contains(out, "cache hit 0%") {
		t.Fatalf("a model with no cache was given a rate:\n%s", out)
	}
}

// A session pays several models at several rates: pricing all of its
// tokens at the session model's rate — which every cost view used to do —
// bills a cheap subagent's work at the expensive model's price, and an
// expensive fallback at the cheap one's.
func TestSessionCostIsPricedModelByModel(t *testing.T) {
	a := &Agent{}
	a.UpdateSettings(func(s *Settings) {
		s.Model = "big"
		s.Price = &llm.Price{Input: 10, Output: 30} // per million
	})
	a.Prices = func(model string) *llm.Price {
		switch model {
		case "big":
			return &llm.Price{Input: 10, Output: 30}
		case "small":
			return &llm.Price{Input: 1, Output: 2}
		}
		return nil
	}
	a.addUsage("big", llm.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})     // $40
	a.addSideUsage("small", llm.Usage{InputTokens: 1_000_000, OutputTokens: 500_000}) // $2
	usd, source, unpriced, known := a.SessionCost()
	if !known || len(unpriced) != 0 || source != "list price" {
		t.Fatalf("cost = %v %q unpriced=%v known=%v", usd, source, unpriced, known)
	}
	if usd != 42 {
		t.Fatalf("cost = %v, want 42 (40 on big, 2 on small)", usd)
	}
	// The old whole-session figure, for comparison: every token at big's
	// rate is $65, half as much again as was actually spent.
	if flat, _ := a.UsageSnapshot().Cost(a.Settings().Price); flat != 65 {
		t.Fatalf("flat figure = %v", flat)
	}

	// A model nothing prices is named, not counted as free.
	a.addUsage("mystery", llm.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	usd, _, unpriced, known = a.SessionCost()
	if !known || usd != 42 || len(unpriced) != 1 || unpriced[0] != "mystery" {
		t.Fatalf("cost = %v unpriced = %v", usd, unpriced)
	}
	if note := a.CostNote(); !strings.Contains(note, "not counted: mystery") {
		t.Fatalf("note = %q", note)
	}

	// A provider that reports its own cost is used as reported, per model.
	b := &Agent{}
	b.addUsage("reported-model", llm.Usage{InputTokens: 10, CostUSD: 0.25})
	usd, source, _, known = b.SessionCost()
	if !known || source != "reported" || usd != 0.25 {
		t.Fatalf("reported cost = %v %q known=%v", usd, source, known)
	}
}
