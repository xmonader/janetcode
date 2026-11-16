package agent

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func usageResp(r *llm.Response, in, out int) *llm.Response {
	r.Usage = llm.Usage{InputTokens: in, OutputTokens: out}
	return r
}

// TestDryRunEstimateMeasuresOnlyTheDryRunAndBoundsTheRest: a call made
// before the dry run is not counted in it; the dry run's own two calls
// are priced at the session's rate; running the one recorded command is
// a range from one call to one per recorded call, priced at the dry run's
// average call, and the basis says so.
func TestDryRunEstimateMeasuresOnlyTheDryRunAndBoundsTheRest(t *testing.T) {
	a, _ := dryAgent(t,
		usageResp(text("before"), 5000, 500),
		usageResp(toolCall("t1", "bash", `{"command":"touch x"}`), 1000_000, 0),
		usageResp(text("done"), 0, 100_000),
	)
	a.Model = "m"
	a.Price = &llm.Price{Input: 2, Output: 10}
	if err := a.Run(context.Background(), "earlier turn", nil); err != nil {
		t.Fatal(err)
	}
	a.SetDryRun(true)
	if err := a.Run(context.Background(), "plan it", nil); err != nil {
		t.Fatal(err)
	}
	plan := a.DryRunPlan()
	e := plan.Estimate
	// 1M input at $2 + 100k output at $10 = $3.00; the earlier turn is excluded.
	if e.SpentCalls != 2 || !e.SpentKnown || math.Abs(e.SpentUSD-3.0) > 1e-9 || e.CostSource != "list price" {
		t.Fatalf("spent = %+v", e)
	}
	if e.ApplyCalls != 0 || e.Pending != 1 || e.FollowCallsLow != 1 || e.FollowCallsHigh != 1 {
		t.Fatalf("follow-up = %+v", e)
	}
	if !e.FollowKnown || math.Abs(e.FollowUSDLow-1.5) > 1e-9 {
		t.Fatalf("follow-up cost = %+v (want the average call, $1.50)", e)
	}
	for _, want := range []string{"list price", "no model call", "average per call", "longer history"} {
		if !strings.Contains(e.Basis, want) {
			t.Errorf("basis lacks %q: %s", want, e.Basis)
		}
	}
	report := a.DryRunReport(false)
	if !strings.Contains(report, "estimate: dry run 2 model call(s) so far, $3.00 (list price)") || !strings.Contains(report, "basis:") {
		t.Fatalf("report lacks the estimate:\n%s", report)
	}
}

// TestDryRunEstimateUnpricedModelGivesNoDollars: with no rate for the
// model the preview says so instead of printing $0.
func TestDryRunEstimateUnpricedModelGivesNoDollars(t *testing.T) {
	a, _ := dryAgent(t,
		toolCall("t1", "bash", `{"command":"touch x"}`),
		text("done"),
	)
	a.SetDryRun(true)
	if err := a.Run(context.Background(), "plan it", nil); err != nil {
		t.Fatal(err)
	}
	e := a.DryRunPlan().Estimate
	if e.SpentCalls != 2 || e.SpentKnown || e.FollowKnown || e.CostSource != "unpriced" {
		t.Fatalf("estimate = %+v", e)
	}
	report := a.DryRunReport(false)
	if !strings.Contains(report, "cost unpriced") || strings.Contains(report, "$0") {
		t.Fatalf("report claims a price it does not have:\n%s", report)
	}
}

// TestDryRunPlanListsHostsAndProfile: the structured plan carries the
// hosts (once each), the recorded MCP tools, the mode and the sandbox.
func TestDryRunPlanListsHostsAndProfile(t *testing.T) {
	a, _ := dryAgent(t)
	a.SetDryRun(true)
	a.recordDry(DryAction{Tool: "fetch_url", Kind: "network", Host: "docs.example.org"})
	a.recordDry(DryAction{Tool: "fetch_url", Kind: "network", Host: "docs.example.org"})
	a.recordDry(DryAction{Tool: "mcp__deploy__ship", Kind: "mcp", Summary: "{}"})
	p := a.DryRunPlan()
	if len(p.Hosts) != 1 || p.Hosts[0] != "docs.example.org" {
		t.Fatalf("hosts = %v", p.Hosts)
	}
	if p.Mode == "" || p.Sandbox != "off" || len(p.Actions) != 3 || p.Files == nil {
		t.Fatalf("plan = %+v", p)
	}
}
