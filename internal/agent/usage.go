package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// ModelUsage is one model's share of the session: a session routes to a
// cheap model, falls back to another when a call fails, asks an advisor
// or a subagent on a third, and the totals alone cannot say which of them
// spent what.
type ModelUsage struct {
	Model string
	Calls int
	Usage llm.Usage
}

// noteModel attributes one call to the model that served it. Called with
// usageMu held.
func (a *Agent) noteModel(model string, u llm.Usage) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(a.Settings().Model)
	}
	if model == "" {
		return
	}
	if a.byModel == nil {
		a.byModel = map[string]*ModelUsage{}
	}
	m := a.byModel[model]
	if m == nil {
		m = &ModelUsage{Model: model}
		a.byModel[model] = m
	}
	m.Calls++
	m.Usage.Add(u)
}

// UsageByModel is what each model spent this session, busiest first.
func (a *Agent) UsageByModel() []ModelUsage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	out := make([]ModelUsage, 0, len(a.byModel))
	for _, m := range a.byModel {
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool {
		ti := out[i].Usage.InputTokens + out[i].Usage.CacheReadTokens + out[i].Usage.CacheWriteTokens + out[i].Usage.OutputTokens
		tj := out[j].Usage.InputTokens + out[j].Usage.CacheReadTokens + out[j].Usage.CacheWriteTokens + out[j].Usage.OutputTokens
		if ti != tj {
			return ti > tj
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// FormatUsageByModel renders the per-model split for /usage, or "" when
// nothing has been recorded. No cost column: the session carries a price
// for the model it is set to, and pricing the others would need a rate
// lookup this package does not have — a column that is right for one row
// and blank for the rest says less than none.
func FormatUsageByModel(rows []ModelUsage) string {
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, r := range rows {
		if n := len(r.Model); n > width {
			width = n
		}
	}
	var sb strings.Builder
	sb.WriteString("per model")
	for _, r := range rows {
		fmt.Fprintf(&sb, "\n  %-*s  %s · %s", width, r.Model, textfmt.Plural(r.Calls, "call"), r.Usage.TokenLine())
	}
	return sb.String()
}

// addUsage accumulates the usage of one model call. The status bar reads it
// from the UI goroutine while the turn runs, so it is guarded.
func (a *Agent) addUsage(model string, u llm.Usage) {
	a.usageMu.Lock()
	a.Usage.Add(u)
	a.turnUse.Add(u)
	a.noteModel(model, u)
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
func (a *Agent) addSideUsage(model string, u llm.Usage) {
	if a == nil {
		return
	}
	a.usageMu.Lock()
	a.Usage.Add(u)
	a.turnUse.Add(u)
	a.noteModel(model, u)
	a.usageMu.Unlock()
}
