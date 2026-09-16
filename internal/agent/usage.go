package agent

import (
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// addUsage accumulates the usage of one model call. The status bar reads it
// from the UI goroutine while the turn runs, so it is guarded.
func (a *Agent) addUsage(u llm.Usage) {
	a.usageMu.Lock()
	a.Usage.Add(u)
	a.turnUse.Add(u)
	// A prefix paid for twice is invisible in the totals; the ledger
	// compares each call with the one before it. A history the harness
	// rewrote (a prune or a compaction) invalidates the prefix for a
	// reason, so it is not counted as waste.
	a.cache.record(u, a.Settings().Model, a.turnN, time.Now(), a.historyRewritten)
	a.historyRewritten = false
	a.usageMu.Unlock()
}

// addSideUsage bills the parent for spend that did not come from this
// conversation's own model call — the turns a subagent ran. Without it a
// session's total, /stats and the cost ceiling all stopped at what the
// main conversation paid, and a run given a budget could delegate its
// way past it (see budget.go, which says the loop asks again after tool
// results because "a subagent runs its own turns").
//
// The prompt-cache ledger is deliberately not told: it compares each
// request with the one before it in *this* conversation, and a child's
// requests are not those.
func (a *Agent) addSideUsage(u llm.Usage) {
	if a == nil {
		return
	}
	a.usageMu.Lock()
	a.Usage.Add(u)
	a.turnUse.Add(u)
	a.usageMu.Unlock()
}
