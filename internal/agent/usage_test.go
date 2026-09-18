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
	out := FormatUsageByModel(rows)
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
