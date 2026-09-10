package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// TestCacheLedger checks the accounting a prompt-cache regression is
// invisible without: a call that re-paid for a prefix it could have read,
// and why.
func TestCacheLedger(t *testing.T) {
	t0 := time.Now()
	var l cacheLedger
	// A healthy pair: the second call reads the whole shared prefix.
	l.record(llm.Usage{InputTokens: 5000}, "m", 1, t0, false)
	l.record(llm.Usage{InputTokens: 200, CacheReadTokens: 5000}, "m", 2, t0.Add(time.Second), false)
	if len(l.misses) != 0 {
		t.Fatalf("a full cache hit is not waste: %+v", l.misses)
	}
	// A miss: the shared prefix was billed fresh.
	l.record(llm.Usage{InputTokens: 5200}, "m", 3, t0.Add(2*time.Second), false)
	if len(l.misses) != 1 {
		t.Fatalf("expected one miss, got %+v", l.misses)
	}
	if l.misses[0].Tokens != 5200 {
		t.Fatalf("missed %d tokens, want the shared 5200", l.misses[0].Tokens)
	}

	// Causes are distinguished, because the fix differs for each.
	var idle cacheLedger
	idle.record(llm.Usage{InputTokens: 100, CacheReadTokens: 9000}, "m", 1, t0, false)
	idle.record(llm.Usage{InputTokens: 9100}, "m", 2, t0.Add(10*time.Minute), false)
	if len(idle.misses) != 1 || !strings.Contains(idle.misses[0].Cause, "idle") {
		t.Fatalf("a long gap should be blamed on the TTL: %+v", idle.misses)
	}

	var swap cacheLedger
	swap.record(llm.Usage{InputTokens: 100, CacheReadTokens: 9000}, "a", 1, t0, false)
	swap.record(llm.Usage{InputTokens: 9100}, "b", 2, t0.Add(time.Second), false)
	if len(swap.misses) != 1 || !strings.Contains(swap.misses[0].Cause, "model changed") {
		t.Fatalf("a model switch re-bills the prompt and should say so: %+v", swap.misses)
	}

	// A history the harness rewrote invalidates the prefix for a reason.
	var pruned cacheLedger
	pruned.record(llm.Usage{InputTokens: 100, CacheReadTokens: 9000}, "m", 1, t0, false)
	pruned.record(llm.Usage{InputTokens: 9100}, "m", 2, t0.Add(time.Second), true)
	if len(pruned.misses) != 0 {
		t.Fatalf("a compaction is not cache waste: %+v", pruned.misses)
	}

	// Breakpoint granularity always differs by a little; that is not news.
	var noise cacheLedger
	noise.record(llm.Usage{InputTokens: 100, CacheReadTokens: 9000}, "m", 1, t0, false)
	noise.record(llm.Usage{InputTokens: 600, CacheReadTokens: 8600}, "m", 2, t0.Add(time.Second), false)
	if len(noise.misses) != 0 {
		t.Fatalf("under the noise floor should not be reported: %+v", noise.misses)
	}
}

func TestCacheReport(t *testing.T) {
	a := &Agent{Model: "m"}
	if got := a.CacheReport(); !strings.Contains(got, "not enough calls") {
		t.Fatalf("got %q", got)
	}
	t0 := time.Now()
	// Caching is known to work here — the first pair hits — so a later
	// miss is blamed on the prefix rather than on the endpoint.
	a.cache.record(llm.Usage{InputTokens: 8000}, "m", 1, t0, false)
	a.cache.record(llm.Usage{InputTokens: 100, CacheReadTokens: 8000}, "m", 2, t0.Add(time.Second), false)
	a.cache.record(llm.Usage{InputTokens: 8200}, "m", 3, t0.Add(2*time.Second), false)
	a.Usage = llm.Usage{InputTokens: 16300, CacheReadTokens: 8000}
	got := a.CacheReport()
	for _, want := range []string{"paid twice", "the prefix changed", "worst calls"} {
		if !strings.Contains(got, want) {
			t.Fatalf("report is missing %q:\n%s", want, got)
		}
	}
}

// TestTurnCacheWasteReportsTheBigOnes: a turn that paid twice for a large
// prefix is reported with its cause; a small difference is not.
func TestTurnCacheWasteReportsTheBigOnes(t *testing.T) {
	a := &Agent{Model: "m"}
	a.turnN = 1
	now := time.Now()
	a.cache.record(llm.Usage{InputTokens: 1000, CacheReadTokens: 50_000}, "m", 1, now, false)
	// Ten minutes later the prefix has expired: the whole of it is billed.
	a.cache.record(llm.Usage{InputTokens: 51_000}, "m", 1, now.Add(10*time.Minute), false)
	n, cause, idle := a.TurnCacheWaste()
	if n < 20_000 {
		t.Fatalf("waste = %d", n)
	}
	if !strings.Contains(cause, "idle") || idle < 9*time.Minute {
		t.Fatalf("cause = %q idle = %s", cause, idle)
	}
	// A later turn with a healthy cache reports nothing.
	a.turnN = 2
	a.cache.record(llm.Usage{InputTokens: 100, CacheReadTokens: 51_000}, "m", 2, now.Add(10*time.Minute), false)
	if n, _, _ := a.TurnCacheWaste(); n != 0 {
		t.Fatalf("a healthy turn reported %d", n)
	}
}
