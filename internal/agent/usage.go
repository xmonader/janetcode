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

// PriceOf is the rate for one model, from the Prices lookup the front end
// wired in, falling back to the session's own price for the session's own
// model. Nil when nothing prices it.
func (a *Agent) PriceOf(model string) *llm.Price {
	s := a.Settings()
	if model == "" || model == s.Model {
		if s.Price != nil {
			return s.Price
		}
	}
	if a.Prices == nil || model == "" {
		return nil
	}
	return a.Prices(model)
}

// SessionCost is what the session has spent, priced model by model. A
// session is rarely one model — it routes small work to a cheap one,
// falls back when a call fails, asks an advisor, and delegates to
// subagents — and pricing the whole of it at the session model's rate,
// which is what every view used to do, is wrong in both directions: a
// subagent's tokens on a cheap model billed at the expensive one's rate,
// and a fallback to an expensive model billed at the cheap one's.
//
// source is "reported" when every model's figure came from the provider,
// "list price" when any of it is an estimate from a rate table, and
// unpriced models are named in unpriced rather than silently counted as
// free. known is false when nothing could be priced at all.
func (a *Agent) SessionCost() (usd float64, source string, unpriced []string, known bool) {
	rows := a.UsageByModel()
	if len(rows) == 0 {
		// Nothing attributed (a session that has not called yet, or an
		// agent built without the split): the old whole-session figure.
		u := a.UsageSnapshot()
		usd, known = u.Cost(a.Settings().Price)
		return usd, u.CostSource(a.Settings().Price), nil, known
	}
	estimated := false
	for _, r := range rows {
		c, ok := r.Usage.Cost(a.PriceOf(r.Model))
		if !ok {
			unpriced = append(unpriced, r.Model)
			continue
		}
		if r.Usage.CostUSD <= 0 {
			estimated = true
		}
		usd += c
		known = true
	}
	source = "reported"
	if estimated {
		source = "list price"
	}
	if !known {
		source = "unpriced"
	}
	return usd, source, unpriced, known
}

// CostNote is the session's cost with where the figure comes from, and
// what to do about any model it could not price.
func (a *Agent) CostNote() string {
	usd, source, unpriced, known := a.SessionCost()
	var note string
	switch {
	case !known:
		note = "cost unpriced: the provider does not report it and no rate is known for this model; add it under \"prices\" in the config file, or wait for the rate table to be fetched"
	case source == "reported":
		note = "cost " + llm.FormatUSDPrecise(usd) + " (reported by the provider)"
	default:
		note = "cost " + llm.FormatUSDPrecise(usd) + " at list price (an API-equivalent estimate from the config's \"prices\" or LiteLLM's rate table, not money spent)"
	}
	if known && len(unpriced) > 0 {
		note += " · not counted: " + strings.Join(unpriced, ", ") + " (no rate known)"
	}
	return note
}

// FormatUsageByModel renders the per-model split for /usage, or "" when
// nothing has been recorded. price is the rate lookup (Agent.PriceOf);
// nil prices nothing, and a row nothing prices says so rather than
// reading as free.
func FormatUsageByModel(rows []ModelUsage, price func(string) *llm.Price) string {
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
		fmt.Fprintf(&sb, "\n  %-*s  %s · %s", width, r.Model, textfmt.Plural(r.Calls, "call"), r.Usage.CacheLine())
		var p *llm.Price
		if price != nil {
			p = price(r.Model)
		}
		if usd, ok := r.Usage.Cost(p); ok {
			sb.WriteString(" · " + llm.FormatUSDPrecise(usd))
			if r.Usage.CostSource(p) != "reported" {
				sb.WriteString(" est.")
			}
		} else {
			sb.WriteString(" · unpriced")
		}
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
