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
