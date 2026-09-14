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

// TurnUsage is what the current turn has cost across all of its model
// calls. The last call's usage is not the turn's: a turn that took four
// calls to finish paid for four, and reporting only the last one makes a
// long turn look like a short one.
func (a *Agent) TurnUsage() llm.Usage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return a.turnUse
}

// startTurn resets the per-turn usage.
func (a *Agent) startTurn() {
	a.usageMu.Lock()
	a.turnUse = llm.Usage{}
	a.usageMu.Unlock()
}

// UsageSnapshot is a copy of the usage so far, safe to read while a turn
// is running.
func (a *Agent) UsageSnapshot() llm.Usage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return a.Usage
}
