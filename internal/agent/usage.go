package agent

import (
	"fmt"
	"github.com/xmonader/janetcode/internal/session"
	"math"
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
		source = u.CostSource(a.Settings().Price)
		for _, d := range a.UsageDelegated() {
			if d.HasCost {
				usd += d.CostUSD
				if !known {
					source = "reported"
				}
				known = true
			}
		}
		return usd, source, nil, known
	}
	estimated := false
	// Delegated CLIs report their own price (claude) or none (codex); a
	// reported figure counts, tokens alone do not.
	for _, d := range a.UsageDelegated() {
		if d.HasCost {
			usd += d.CostUSD
			known = true
		}
	}
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
	if len(a.UsageByModel()) == 0 && a.UsageSnapshot() == (llm.Usage{}) && len(a.UsageDelegated()) == 0 {
		// Nothing has been called, so nothing has been spent. Saying
		// "unpriced ... add it under prices" here sent people to fix a
		// config that was fine, before the rate table had even loaded.
		return "cost $0 · no model calls yet"
	}
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
	if d := a.UsageDelegated(); len(d) > 0 {
		var usd float64
		unknown := false
		for _, r := range d {
			if r.HasCost {
				usd += r.CostUSD
			} else {
				unknown = true
			}
		}
		note += " · includes delegated " + llm.FormatUSDPrecise(usd)
		if unknown {
			note += " (some delegates report no price)"
		}
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

// addUsage bills a model call whose usage no assistant message carries to
// the log: it is counted and logged as a usage event, so a resumed session
// restores it (RestoreFromLog).
func (a *Agent) addUsage(model string, u llm.Usage) {
	a.countUsage(model, u)
	a.logUsage(model, u)
}

// logUsage records spend the session log would otherwise not have.
func (a *Agent) logUsage(model string, u llm.Usage) {
	if u == (llm.Usage{}) {
		return
	}
	a.logEvent(session.Event{Type: session.TypeUsage, Usage: &u, Model: model})
}

// countUsage accumulates the usage of one model call. The status bar reads it
// from the UI goroutine while the turn runs, so it is guarded.
func (a *Agent) countUsage(model string, u llm.Usage) {
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
	a.logUsage(model, u)
}

// DelegateUsage is one external agent CLI's share of the session. HasCost
// is false when the CLI reports tokens but no price (codex): the cost is
// then unknown, not zero.
type DelegateUsage struct {
	Agent        string
	Runs         int
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	HasCost      bool
}

const delegatePrefix = "delegate:"

// AddDelegateUsage counts one delegate run in the session: its own bucket
// (never Usage, so cache-hit stats stay the model's), the session cost,
// and the shared --max-cost account, so a run over its ceiling because of
// a delegate stops like any other overrun. It is logged so a resume
// restores it.
func (a *Agent) AddDelegateUsage(name string, in, out int, costUSD float64, hasCost bool) {
	if a == nil || (in <= 0 && out <= 0 && !hasCost) {
		return
	}
	in, out = max(in, 0), max(out, 0)
	if !(costUSD > 0) || math.IsInf(costUSD, 0) {
		costUSD, hasCost = 0, false
	}
	u := llm.Usage{InputTokens: in, OutputTokens: out, CostUSD: costUSD}
	// budgetMu then usageMu, as sharedAccount takes them: a budget built
	// concurrently either sees this run in SessionCost or gets it added.
	a.budgetMu.Lock()
	a.noteDelegate(name, u)
	if b := a.budget; b != nil && hasCost {
		b.mu.Lock()
		b.spent += costUSD
		b.known = true
		b.mu.Unlock()
	}
	a.budgetMu.Unlock()
	a.logUsage(delegatePrefix+name, u)
}

func (a *Agent) noteDelegate(name string, u llm.Usage) {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	if a.delegated == nil {
		a.delegated = map[string]*DelegateUsage{}
	}
	d := a.delegated[name]
	if d == nil {
		d = &DelegateUsage{Agent: name}
		a.delegated[name] = d
	}
	d.Runs++
	d.InputTokens += u.InputTokens
	d.OutputTokens += u.OutputTokens
	if u.CostUSD > 0 {
		d.CostUSD += u.CostUSD
		d.HasCost = true
	}
}

// UsageDelegated is what each delegate CLI spent this session, by name.
func (a *Agent) UsageDelegated() []DelegateUsage {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	out := make([]DelegateUsage, 0, len(a.delegated))
	for _, d := range a.delegated {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

// FormatUsageDelegated renders the delegated bucket for /usage, or "".
func FormatUsageDelegated(rows []DelegateUsage) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("delegated (separate from the model's tokens and cache stats)")
	for _, r := range rows {
		fmt.Fprintf(&sb, "\n  %s  %s · ↑%s ↓%s", r.Agent, textfmt.Plural(r.Runs, "run"), textfmt.Tokens(r.InputTokens), textfmt.Tokens(r.OutputTokens))
		if r.HasCost {
			sb.WriteString(" · " + llm.FormatUSDPrecise(r.CostUSD))
		} else {
			sb.WriteString(" · cost unknown (the CLI reports no price)")
		}
	}
	return sb.String()
}

// restoreUsage sets the session totals from its log: every assistant
// message's usage and every usage event. A resume started them at zero,
// so ↑/↓, the cost and /usage read as if the session had spent nothing.
func (a *Agent) restoreUsage(ev []session.Event) {
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	a.Usage = llm.Usage{}
	a.byModel = nil
	a.delegated = nil
	for _, e := range ev {
		if e.Usage == nil || (e.Type != session.TypeAssistant && e.Type != session.TypeUsage) {
			continue
		}
		if name, ok := strings.CutPrefix(e.Model, delegatePrefix); ok && e.Type == session.TypeUsage {
			// noteDelegate takes usageMu, which is held here.
			a.usageMu.Unlock()
			a.noteDelegate(name, *e.Usage)
			a.usageMu.Lock()
			continue
		}
		a.Usage.Add(*e.Usage)
		a.noteModel(e.Model, *e.Usage)
	}
}

// cancelledUsage estimates what a model call cancelled mid-stream cost.
// The provider had read the request and was generating, so both sides
// were billed, but a cancelled stream ends before the frame that carries
// the usage and every provider returns none. Leaving it out made the
// session's ↑/↓ counts and its cost claim that the cut-off answer was
// free. reqTokens is the request estimate the call was made with; out
// and thinking are what it streamed.
func cancelledUsage(reqTokens int, out, thinking string) llm.Usage {
	think := estimateTokens(thinking)
	return llm.Usage{
		InputTokens:     max(reqTokens, 0),
		OutputTokens:    estimateTokens(out) + think,
		ReasoningTokens: think,
	}
}
