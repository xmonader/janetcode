package agent

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// What a subagent spent thinking is part of what it spent: the delta
// billed to the parent carries every field of the usage, reasoning
// tokens included, so /usage's "(N thinking)" counts delegated work too.
func TestUsageSinceKeepsEveryField(t *testing.T) {
	before := llm.Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2, ReasoningTokens: 1, CostUSD: 0.5}
	now := llm.Usage{InputTokens: 30, OutputTokens: 25, CacheReadTokens: 13, CacheWriteTokens: 12, ReasoningTokens: 21, CostUSD: 1.5}
	want := llm.Usage{InputTokens: 20, OutputTokens: 20, CacheReadTokens: 10, CacheWriteTokens: 10, ReasoningTokens: 20, CostUSD: 1}
	if got := usageSince(before, now); got != want {
		t.Fatalf("usageSince = %+v, want %+v", got, want)
	}
}
