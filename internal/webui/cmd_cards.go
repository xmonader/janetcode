package webui

import (
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// The structured companions of /perf and /usage: the same numbers the
// text sheets print, sent as JSON so the front-end can draw them —
// a stacked wall-clock bar, per-model usage rows — instead of laying
// ASCII columns into a proportional font.

type perfSliceCard struct {
	Name      string `json:"name"`
	Ms        int64  `json:"ms"`
	Pct       int    `json:"pct"` // share of the session's open wall clock
	Count     int    `json:"count,omitempty"`
	SlowestMs int64  `json:"slowest_ms,omitempty"`
	Note      string `json:"note,omitempty"`
}

type perfCard struct {
	OpenMs int64           `json:"open_ms"`
	TurnMs int64           `json:"turn_ms"`
	IdleMs int64           `json:"idle_ms"` // reading and typing, outside every turn
	Slices []perfSliceCard `json:"slices"`
}

func (s *server) perfCard() perfCard {
	st := s.opts.Agent.Stats()
	open := time.Since(s.openedAt)
	out := perfCard{
		OpenMs: open.Milliseconds(),
		TurnMs: st.TurnTime.Milliseconds(),
	}
	out.IdleMs = out.OpenMs - out.TurnMs
	if out.IdleMs < 0 {
		out.IdleMs = 0
	}
	for _, sl := range st.Perf() {
		c := perfSliceCard{
			Name:      sl.Name,
			Ms:        sl.Time.Milliseconds(),
			Count:     sl.Count,
			SlowestMs: sl.Slowest.Milliseconds(),
			Note:      sl.Note,
		}
		if open > 0 {
			c.Pct = int(sl.Time * 100 / open)
		}
		out.Slices = append(out.Slices, c)
	}
	return out
}

type usageModelCard struct {
	Model      string  `json:"model"`
	Calls      int     `json:"calls"`
	In         int     `json:"in"`
	CacheRead  int     `json:"cache_read"`
	CacheWrite int     `json:"cache_write"`
	Out        int     `json:"out"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

type usageCard struct {
	Models   []usageModelCard `json:"models"`
	In       int              `json:"in"`
	Cache    int              `json:"cache"` // read + written
	Out      int              `json:"out"`
	CostUSD  float64          `json:"cost_usd,omitempty"`
	CachePct int              `json:"cache_pct,omitempty"`
	Ceiling  float64          `json:"ceiling,omitempty"`
	// Delegated is kept apart from Models: those tokens are not the
	// model's and would skew its cache-hit figure.
	Delegated []usageDelegateCard `json:"delegated,omitempty"`
}

type usageDelegateCard struct {
	Agent   string  `json:"agent"`
	Runs    int     `json:"runs"`
	In      int     `json:"in"`
	Out     int     `json:"out"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	HasCost bool    `json:"has_cost"`
}

func (s *server) usageCard() usageCard {
	ag := s.opts.Agent
	byModel := ag.UsageByModel()
	out := usageCard{Ceiling: ag.MaxCost(), Models: make([]usageModelCard, 0, len(byModel))}
	var total llm.Usage
	for _, m := range byModel {
		u := m.Usage
		total.Add(u)
		out.Models = append(out.Models, usageModelCard{
			Model:      m.Model,
			Calls:      m.Calls,
			In:         u.InputTokens,
			CacheRead:  u.CacheReadTokens,
			CacheWrite: u.CacheWriteTokens,
			Out:        u.OutputTokens,
			CostUSD:    u.CostUSD,
		})
	}
	for _, d := range ag.UsageDelegated() {
		out.Delegated = append(out.Delegated, usageDelegateCard{Agent: d.Agent, Runs: d.Runs, In: d.InputTokens, Out: d.OutputTokens, CostUSD: d.CostUSD, HasCost: d.HasCost})
		out.CostUSD += d.CostUSD
	}
	out.In = total.InputTokens
	out.Cache = total.CacheReadTokens + total.CacheWriteTokens
	out.Out = total.OutputTokens
	out.CostUSD = total.CostUSD
	if pct, ok := total.CacheHit(); ok {
		out.CachePct = pct
	}
	return out
}
