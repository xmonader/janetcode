package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// DryEstimate is what the dry run cost and what carrying the plan out is
// likely to cost, with the basis for every figure. The first half is
// measured (the calls the dry run made, priced); the second half is a
// range, because how many calls the model needs to run the commands it
// planned is the model's choice, not something the preview can know.
type DryEstimate struct {
	// SpentCalls and SpentUSD are what the dry run itself used so far:
	// measured from the session's usage since the dry run began.
	SpentCalls int     `json:"spent_calls"`
	SpentUSD   float64 `json:"spent_usd"`
	// SpentKnown is false when no model used in the dry run can be priced.
	SpentKnown bool `json:"spent_known"`
	// CostSource is "reported", "list price" or "unpriced", as everywhere
	// else cost is shown.
	CostSource string `json:"cost_source"`
	// ApplyCalls is the model calls applying the files takes: none, the
	// files are written from the preview.
	ApplyCalls int `json:"apply_calls"`
	// Pending counts the recorded calls that still have to be made for
	// real (commands, MCP tools, subagents, other; not hooks, which run on
	// their own once the dry run is over).
	Pending int `json:"pending_calls"`
	// FollowCallsLow and FollowCallsHigh bound the model calls needed to
	// run them: one call can carry several tool calls, so the low end is
	// one; the high end is one per call, each followed by a model turn.
	FollowCallsLow  int `json:"follow_calls_low"`
	FollowCallsHigh int `json:"follow_calls_high"`
	// FollowUSDLow and FollowUSDHigh price those calls at the dry run's
	// own measured cost per call; FollowKnown is false without one.
	FollowUSDLow  float64 `json:"follow_usd_low"`
	FollowUSDHigh float64 `json:"follow_usd_high"`
	FollowKnown   bool    `json:"follow_known"`
	// Basis says in words where the numbers come from and how far to
	// trust them.
	Basis string `json:"basis"`
}

// DryPlan is the whole preview as data: what the text report and the
// result object of a headless run, and the web UI's panel, all show.
type DryPlan struct {
	Mode     string      `json:"mode"`
	Sandbox  string      `json:"sandbox"`
	Verify   []string    `json:"verify,omitempty"`
	Files    []DryFile   `json:"files"`
	Actions  []DryAction `json:"actions"`
	Hosts    []string    `json:"hosts"`
	Estimate DryEstimate `json:"estimate"`
}

// DryRunPlan is the preview as one structure.
func (a *Agent) DryRunPlan() DryPlan {
	p := DryPlan{
		Mode:    a.Settings().ModeName(),
		Sandbox: "off",
		Files:   a.dryFiles(),
		Actions: a.DryRunActions(),
		Hosts:   []string{},
	}
	if a.Sandbox.Enabled() {
		p.Sandbox = a.Sandbox.ProfileName()
	}
	if a.Verify.Enabled() {
		p.Verify = a.Verify.CommandNames()
	}
	if p.Files == nil {
		p.Files = []DryFile{}
	}
	if p.Actions == nil {
		p.Actions = []DryAction{}
	}
	seen := map[string]bool{}
	for _, x := range p.Actions {
		if x.Kind == "network" && x.Host != "" && !seen[x.Host] {
			seen[x.Host] = true
			p.Hosts = append(p.Hosts, x.Host)
		}
	}
	p.Estimate = a.dryEstimate(p.Actions)
	return p
}

// dryEstimate measures what the dry run used and bounds what the plan
// still needs. It never invents a precision it does not have: an
// unpriced model gives calls but no dollars, and the follow-up cost is a
// range from the dry run's own average call.
func (a *Agent) dryEstimate(acts []DryAction) DryEstimate {
	d := a.dryState()
	d.mu.Lock()
	base := d.usageBase
	d.mu.Unlock()

	est := DryEstimate{CostSource: "unpriced"}
	var usd float64
	estimated, priced := false, false
	for _, cur := range a.UsageByModel() {
		b := base[cur.Model]
		calls := cur.Calls - b.Calls
		if calls <= 0 {
			continue
		}
		est.SpentCalls += calls
		u := cur.Usage
		u.InputTokens -= b.Usage.InputTokens
		u.OutputTokens -= b.Usage.OutputTokens
		u.CacheReadTokens -= b.Usage.CacheReadTokens
		u.CacheWriteTokens -= b.Usage.CacheWriteTokens
		u.CostUSD -= b.Usage.CostUSD
		c, ok := u.Cost(a.PriceOf(cur.Model))
		if !ok {
			continue
		}
		priced = true
		if u.CostUSD <= 0 {
			estimated = true
		}
		usd += c
	}
	if priced {
		est.SpentKnown, est.SpentUSD = true, usd
		est.CostSource = "reported"
		if estimated {
			est.CostSource = "list price"
		}
	}
	for _, x := range acts {
		switch x.Kind {
		case "file", "network", "hook":
		default:
			est.Pending++
		}
	}
	if est.Pending > 0 {
		est.FollowCallsLow, est.FollowCallsHigh = 1, est.Pending
		if est.SpentKnown && est.SpentCalls > 0 {
			per := est.SpentUSD / float64(est.SpentCalls)
			est.FollowKnown = true
			est.FollowUSDLow, est.FollowUSDHigh = per*float64(est.FollowCallsLow), per*float64(est.FollowCallsHigh)
		}
	}
	var b strings.Builder
	switch {
	case est.SpentCalls == 0:
		b.WriteString("the dry run has made no model calls yet, so there is no measured cost per call")
	case est.SpentKnown:
		fmt.Fprintf(&b, "dry run: %d model call(s), measured from session usage since it began, priced %s", est.SpentCalls, costBasis(est.CostSource))
	default:
		fmt.Fprintf(&b, "dry run: %d model call(s); no rate is known for the model, so no dollar figure (add it under \"prices\" in the config)", est.SpentCalls)
	}
	b.WriteString("; applying the files makes no model call")
	if est.Pending > 0 {
		fmt.Fprintf(&b, "; running the %d recorded call(s) takes the model between %d and %d more call(s) (it may batch them), each estimated at the dry run's average per call, though later calls carry a longer history and cost more, and a command that fails costs further calls", est.Pending, est.FollowCallsLow, est.FollowCallsHigh)
	}
	est.Basis = b.String()
	return est
}

func costBasis(source string) string {
	if source == "reported" {
		return "as the provider reported it"
	}
	return "at list price, an API-equivalent estimate and not money spent"
}

// describe is the estimate on the lines the report prints.
func (e DryEstimate) describe() string {
	var sb strings.Builder
	spent := fmt.Sprintf("%d model call(s) so far", e.SpentCalls)
	if e.SpentKnown {
		spent += ", " + llm.FormatUSDPrecise(e.SpentUSD) + " (" + e.CostSource + ")"
	} else if e.SpentCalls > 0 {
		spent += ", cost unpriced"
	}
	fmt.Fprintf(&sb, "estimate: dry run %s · applying files: 0 calls", spent)
	if e.Pending > 0 {
		fmt.Fprintf(&sb, " · running the %d recorded call(s): %d-%d more call(s)", e.Pending, e.FollowCallsLow, e.FollowCallsHigh)
		if e.FollowKnown {
			fmt.Fprintf(&sb, ", roughly %s-%s", llm.FormatUSDPrecise(e.FollowUSDLow), llm.FormatUSDPrecise(e.FollowUSDHigh))
		}
	}
	sb.WriteString("\n  basis: " + e.Basis)
	return sb.String()
}
