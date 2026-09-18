package tui

import (
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
)

// A cost ceiling was wired only from the headless flag, so the one place
// a session runs long enough to want one — a TUI session left working on
// something — was the one place it could not be set. /budget sets it for
// this session, with the same behaviour headless has: the turn is stopped
// as soon as the spend crosses the line, tools the model had asked for
// are answered rather than run, and the run in flight may overshoot by
// the model call that was already going when it crossed.
func (m *model) budgetCommand(arg string) {
	a := m.opts.Agent
	switch {
	case arg == "":
		if a.MaxCost() <= 0 {
			m.note("no cost budget · /budget 0.50 stops the session once it has spent that much")
			return
		}
		m.note("cost budget " + m.budgetStatus() + " · /budget off removes it")
	default:
		usd, err := agent.ParseBudget(arg)
		if err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		if usd == 0 {
			if a.MaxCost() <= 0 {
				m.note("there was no cost budget")
				return
			}
			a.SetMaxCost(0)
			m.note("cost budget removed")
			return
		}
		a.SetMaxCost(usd)
		line := "cost budget " + m.budgetStatus() + " · the session stops when it crosses that, give or take the model call in flight"
		snap := a.UsageSnapshot()
		if _, _, _, known := a.SessionCost(); !known && snap.InputTokens+snap.OutputTokens > 0 {
			// A ceiling measured against an unknown spend never fires,
			// and silently doing nothing is the worst way to say so.
			// Only once there has been a call, though: before the first
			// one the spend is zero and unpriceable for every model, and
			// warning then told everyone who set a budget up front that
			// it would not work — while the status line went on showing
			// the provider's own figure against the ceiling.
			line += "\nnothing prices this model, so the spend is unknown and the budget cannot stop anything · /usage says why"
		}
		if strings.Contains(line, "\n") {
			m.transcript.info(line)
		} else {
			m.note(line)
		}
	}
}

// budgetStatus is the spend against the ceiling, for the status line and
// for /budget itself.
func (m *model) budgetStatus() string {
	a := m.opts.Agent
	s := llm.FormatUSDPrecise(a.MaxCost())
	if spent, _, _, known := a.SessionCost(); known {
		s = llm.FormatUSDPrecise(spent) + " of " + s
	}
	return s
}
