package agent

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// ErrBudget is returned when a run has spent its ceiling. The headless
// runner maps it to exit 55.
var ErrBudget = errors.New("the run reached its cost budget")

// A ceiling that is only looked at between turns is not a ceiling. One
// turn can make fifty model calls with a large context on each, so a run
// given a $1 budget could return having spent several, and the caller
// would learn about it from the invoice. The loop therefore asks after
// every model call and again after the tool results, before the next
// call; the worst overshoot is the call in flight when the line was
// crossed.
//
// overBudget reports what has been spent and whether that is over
// MaxCostUSD. It is false when nothing bounds the run, and false when the
// spend is unknown: an unpriced model cannot be measured against a
// ceiling, and stopping a run on a figure of zero would be worse than
// letting it finish.
func (a *Agent) overBudget() (spent float64, over bool) {
	ceiling := a.MaxCost()
	if ceiling <= 0 {
		return 0, false
	}
	// Priced model by model: a ceiling measured at the session model's
	// rate over tokens a cheap subagent spent is not the spend it is
	// bounding.
	usd, _, _, ok := a.SessionCost()
	if !ok {
		return 0, false
	}
	return usd, usd >= ceiling
}

// budgetErr is what a run that ran out of money returns. The figures are
// formatted precisely rather than compactly: a small ceiling rounds to
// "spent $0.0000 of $0.0000" under the usual money format, which says
// nothing about the thing it is reporting.
func (a *Agent) budgetErr(spent float64) error {
	return fmt.Errorf("%w: spent %s of %s", ErrBudget, llm.FormatUSDPrecise(spent), llm.FormatUSDPrecise(a.MaxCost()))
}

// stopForBudget ends the turn on the budget. When the model had asked for
// tools they are answered rather than run: the history has to stay paired
// whatever happens, and running them would be work paid for by a run that
// is already over its ceiling.
func (a *Agent) stopForBudget(spent float64, uses []llm.Block, emit func(Event)) error {
	err := a.budgetErr(spent)
	a.answerUnrun(uses, "the cost budget is spent", emit)
	// The returned error carries the figures and every front end prints
	// it; a notice and a verdict repeating them made one stop read four
	// times in a row.
	emit(Event{Kind: EvVerdict, Text: "stopped on the cost budget", IsError: true})
	return err
}

// answerUnrun answers, without running, the tool calls of a reply the
// turn is ending on: the reply is already in the history, and a tool_use
// with no tool_result is something every provider refuses, so the next
// turn would fail before it started. why finishes "<tool> was not run:".
func (a *Agent) answerUnrun(uses []llm.Block, why string, emit func(Event)) {
	if len(uses) == 0 {
		return
	}
	results := make([]llm.Block, 0, len(uses))
	for _, u := range uses {
		call := ToolCall{ID: u.ID, Name: u.Name, Input: u.Input}
		msg := u.Name + " was not run: " + why + "."
		results = append(results, llm.ToolResult(replyID(u), msg, true))
		emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
		a.logEvent(session.Event{Type: session.TypeToolResult, CallID: u.ID, Name: u.Name, Arguments: string(u.Input), Output: msg, IsError: true})
	}
	tr := llm.ToolResultMessage(results...)
	a.appendMessage(tr)
	// The answers go on the log as a surface event too: without it a
	// resumed session folds to an assistant turn whose tool_use blocks
	// have no tool_result, which every provider refuses.
	a.logEvent(session.Event{Type: session.TypeToolResult, Surface: true, Message: &tr})
}

// ParseBudget reads a /budget argument: an amount in dollars (a leading $
// is allowed), or off, none or 0 for no ceiling, which returns 0.
func ParseBudget(arg string) (float64, error) {
	arg = strings.TrimSpace(arg)
	if strings.EqualFold(arg, "off") || strings.EqualFold(arg, "none") || arg == "0" {
		return 0, nil
	}
	usd, err := strconv.ParseFloat(strings.TrimPrefix(arg, "$"), 64)
	// ParseFloat also reads "nan" and "inf". NaN is not <= 0, and no
	// spend is ever >= NaN, so it would read as a ceiling while bounding
	// nothing; neither is an amount.
	if err != nil || !(usd > 0) || math.IsInf(usd, 0) {
		return 0, fmt.Errorf("%q is not an amount · /budget 0.50, or /budget off", arg)
	}
	return usd, nil
}
