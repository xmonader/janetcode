package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

func hooksReport(a *agent.Agent) string {
	if a == nil || a.Hooks == nil || a.Hooks.Config.Len() == 0 {
		return "no hooks are configured"
	}
	events := make([]string, 0, len(a.Hooks.Config.Hooks))
	for e := range a.Hooks.Config.Hooks {
		events = append(events, e)
	}
	sort.Strings(events)
	var b strings.Builder
	lim := a.Hooks.Config.Limits.WithDefaults()
	fmt.Fprintf(&b, "hooks run in %s (limits: %ds timeout, %ds for a project hook, %d bytes of output, %d per event):\n", a.Hooks.Cwd, lim.Timeout, lim.ProjectTimeout, lim.Output, lim.PerEvent)
	for _, e := range events {
		for _, h := range a.Hooks.Config.For(e) {
			fmt.Fprintf(&b, "  %-14s %s\n", e, textfmt.Clip(strings.TrimSpace(h.Command), 80))
		}
	}
	return b.String()
}

func verifyReport(a *agent.Agent) string {
	if a == nil || a.Verify == nil || !a.Verify.Enabled() {
		return "no verification is configured for this project"
	}
	c := a.Verify.Config
	var b strings.Builder
	b.WriteString("a turn is not done until these pass:\n")
	for _, cmd := range c.Commands {
		fmt.Fprintf(&b, "  %s\n", cmd)
	}
	if c.Timeout > 0 {
		fmt.Fprintf(&b, "timeout %ds per command\n", c.Timeout)
	}
	if strings.TrimSpace(c.Mutate) != "" {
		fmt.Fprintf(&b, "\nmutation testing with: %s\n", c.Mutate)
		if c.MaxMutants > 0 {
			fmt.Fprintf(&b, "at most %d mutants per run\n", c.MaxMutants)
		}
	}
	return b.String()
}

func providersReport(cfg *config.Config, a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	var b strings.Builder
	name := "(none)"
	if a.Provider != nil {
		name = a.Provider.Name()
	}
	fmt.Fprintf(&b, "provider   %s\n", name)
	fmt.Fprintf(&b, "model      %s\n", a.Model)
	fmt.Fprintf(&b, "max tokens %d\n", a.MaxTokens)
	if cfg != nil {
		if m := cfg.MetaFor(a.Model); m.ContextWindow > 0 {
			fmt.Fprintf(&b, "window     %s\n", tokensOf(m.ContextWindow))
		}
		if p := cfg.PriceFor(a.Model); p != nil {
			fmt.Fprintf(&b, "price      $%.2f in / $%.2f out per million\n", p.Input, p.Output)
		}
	}
	u := a.UsageSnapshot()
	if pct, ok := a.CacheRate(); ok {
		fmt.Fprintf(&b, "cache      %d%% of input served from cache\n", pct)
	}
	if usd, ok := u.Cost(a.Price); ok {
		fmt.Fprintf(&b, "spent      %s\n", llm.FormatUSD(usd))
	}
	if cfg != nil {
		if routes := cfg.RoutableModels(); len(routes) > 0 {
			b.WriteString("\nmodels reachable by name (task can send a job to one):\n")
			for _, r := range routes {
				fmt.Fprintf(&b, "  %-24s %-18s %s\n", r.Model, r.Provider, r.Why)
			}
		}
		b.WriteString("\nany other name is routed by its family (glm-* to Zhipu, claude-* to Anthropic…)\n")
		b.WriteString("when that provider is configured, and otherwise to the active one.\n")
	}
	return b.String()
}

// scriptsReport shows what execute_code actually did. A script is the one
// place where tool calls happen without their output passing in front of
// the model, so it is the one place where the log is the only record of
// what the session did on the user's machine.
func scriptsReport(a *agent.Agent) string {
	if a == nil || a.Log == nil {
		return "no agent"
	}
	return agent.RenderScripts(a.Log.Events())
}

func todosReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	return agent.RenderTodos(a.Todos())
}

func goalsReport(a *agent.Agent) string {
	if a == nil || a.Log == nil {
		return "no session log"
	}
	gs := agent.FoldGoals(a.Log.Events())
	if len(gs) == 0 {
		return "no goals in this session"
	}
	var b strings.Builder
	for _, g := range gs {
		fmt.Fprintf(&b, "  %-5s %-9s %d/%d rounds  %s\n", g.ID, g.Phase, g.Rounds, g.MaxRounds, textfmt.Clip(strings.TrimSpace(g.Objective), 70))
		if g.Reason != "" {
			fmt.Fprintf(&b, "        %s\n", textfmt.Clip(strings.TrimSpace(g.Reason), 80))
		}
	}
	return b.String()
}

func workingReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	if r := a.WorkingMemory(); strings.TrimSpace(r) != "" {
		return r
	}
	return "the working record is empty (nothing has been compacted yet)"
}

func toolsReport(a *agent.Agent) string {
	if a == nil || a.Tools == nil {
		return "no tools"
	}
	defs := a.Tools.Defs()
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	var b strings.Builder
	total := 0
	for _, d := range defs {
		total += len(d.Description) + len(d.InputSchema)
	}
	fmt.Fprintf(&b, "%d tools, ~%s of schema in every request\n", len(defs), tokensOf(total/4))
	for _, d := range defs {
		mutating := ""
		if t, ok := a.Tools.Get(d.Name); ok && t.Mutating() {
			mutating = "  (asks first)"
		}
		fmt.Fprintf(&b, "  %-18s %6s%s\n", d.Name, tokensOf((len(d.Description)+len(d.InputSchema))/4), mutating)
	}
	return b.String()
}

func statsReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	s := a.Stats()
	calls, errs := s.ToolCalls()
	var b strings.Builder
	fmt.Fprintf(&b, "%d turns · %d model calls · %d tool calls (%d failed) · %d compactions\n",
		s.Turns, s.ModelCalls, calls, errs, s.Compactions)
	if s.ModelTime > 0 {
		fmt.Fprintf(&b, "model time %s (%d failed calls)\n", s.ModelTime.Round(time.Millisecond), s.ModelErrors)
	}
	// Tokens and money, the same figures /stats shows a user. Asked how
	// much a session had cost, the model called inspect stats — which is
	// what the system prompt points it at — read a report of turns and
	// timings with no money in it anywhere, and told the user janetcode
	// does not track cost. It does; this subject simply was not saying.
	u := a.UsageSnapshot()
	if u.InputTokens+u.OutputTokens > 0 {
		fmt.Fprintf(&b, "tokens · in %d · out %d · cache read %d · cache write %d",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
		if usd, ok := u.Cost(a.Price); ok {
			b.WriteString(" · cost " + llm.FormatUSD(usd) + " (" + u.CostSource(a.Price) + ")")
		} else {
			b.WriteString(" · cost unknown: nothing prices this model")
		}
		if a.MaxCostUSD > 0 {
			b.WriteString(" · budget " + llm.FormatUSDPrecise(a.MaxCostUSD))
		}
		b.WriteString("\n")
	}
	if s.Saves() > 0 {
		fmt.Fprintf(&b, "recovered: %d tool calls from text the model wrote instead of calling · %d streams ended by the watchdog · %d calls that only worked on a fallback model\n",
			s.Recoveries, s.WatchdogKills, s.Fallbacks)
	}
	if len(s.Tools) > 0 {
		b.WriteString("\n  tool                calls  errors      time\n")
		for _, t := range s.Tools {
			fmt.Fprintf(&b, "  %-18s %6d  %6d  %8s\n", t.Name, t.Calls, t.Errors, t.Time.Round(time.Millisecond))
		}
	}
	return b.String()
}

func trajectoryReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	f := a.Trajectory()
	var b strings.Builder
	b.WriteString("the features the critic scores this turn on:\n")
	for _, n := range agent.FeatureNames {
		fmt.Fprintf(&b, "  %-18s %10.2f\n", n, f[n])
	}
	if p := a.Predictor; p != nil {
		fmt.Fprintf(&b, "\ncritic trained on %d runs, %.0f%% cross-validated (guessing the majority: %.0f%%)\n",
			p.Samples, p.Accuracy*100, p.Baseline*100)
	} else {
		b.WriteString("\nno trained critic is loaded, so these are recorded but not scored\n")
	}
	return b.String()
}

func permissionsReport(a *agent.Agent, cfg *config.Config) string {
	if a == nil {
		return "no agent"
	}
	var b strings.Builder
	switch {
	case a.ReadOnly:
		b.WriteString("read-only: nothing that changes files or runs commands is offered\n")
	case a.AutoApprove:
		b.WriteString("auto-approve is ON: mutating tools run without asking\n")
	case a.AutoMode:
		b.WriteString("auto mode: in-workspace edits and known-safe reads run; other commands ask\n")
	default:
		b.WriteString("edits and commands ask first\n")
	}
	if a.PlanMode {
		b.WriteString("plan mode: the model may look but not change\n")
	}
	rules := a.Rules
	if len(rules.Allow) == 0 && len(rules.Deny) == 0 {
		b.WriteString("\nno standing rules; every mutating call is asked about\n")
		return b.String()
	}
	if len(rules.Allow) > 0 {
		b.WriteString("\nallowed without asking:\n")
		for _, r := range rules.Allow {
			fmt.Fprintf(&b, "  %s\n", r)
		}
	}
	if len(rules.Deny) > 0 {
		b.WriteString("\nalways denied:\n")
		for _, r := range rules.Deny {
			fmt.Fprintf(&b, "  %s\n", r)
		}
	}
	return b.String()
}

func undoReport(a *agent.Agent) string {
	if a == nil {
		return "no agent"
	}
	changes := a.Changes()
	if len(changes) == 0 {
		return "no file changes to undo"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d file changes recorded (newest last):\n", len(changes))
	for i, c := range changes {
		fmt.Fprintf(&b, "  %-3d turn %-3d %-10s %s\n", i+1, c.Turn, c.Tool, c.Path)
		if c.Intent != "" {
			fmt.Fprintf(&b, "      %s\n", textfmt.Clip(strings.TrimSpace(c.Intent), 90))
		}
	}
	return b.String()
}
