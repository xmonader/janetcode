package agent

import (
	"errors"
	"fmt"

	"github.com/xmonader/janetcode/internal/llm"
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
	if a.MaxCostUSD <= 0 {
		return 0, false
	}
	usd, ok := a.UsageSnapshot().Cost(a.Price)
	if !ok {
		return 0, false
	}
	return usd, usd >= a.MaxCostUSD
}

// budgetErr is what a run that ran out of money returns.
func (a *Agent) budgetErr(spent float64) error {
	return fmt.Errorf("%w: spent %s of %s", ErrBudget, llm.FormatUSD(spent), llm.FormatUSD(a.MaxCostUSD))
}

// stopForBudget ends the turn on the budget. When the model had asked for
// tools they are answered rather than run: the history has to stay paired
// whatever happens, and running them would be work paid for by a run that
// is already over its ceiling.
func (a *Agent) stopForBudget(spent float64, uses []llm.Block, emit func(Event)) error {
	err := a.budgetErr(spent)
	if len(uses) > 0 {
		results := make([]llm.Block, 0, len(uses))
		for _, u := range uses {
			call := ToolCall{ID: u.ID, Name: u.Name, Input: u.Input}
			msg := u.Name + " was not run: " + err.Error() + "."
			results = append(results, llm.ToolResult(replyID(u), msg, true))
			emit(Event{Kind: EvToolResult, Call: &call, Output: msg, IsError: true})
		}
		tr := llm.ToolResultMessage(results...)
		a.Messages = append(a.Messages, tr)
	}
	emit(Event{Kind: EvNotice, Text: err.Error()})
	emit(Event{Kind: EvVerdict, Text: "stopped on the cost budget · " + err.Error(), IsError: true})
	return err
}
