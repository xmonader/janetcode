package agent

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// ErrBudget is returned when a run has spent its ceiling, or when a
// persistent daily or monthly spend cap (internal/spend) is reached. The
// headless runner maps it to exit 55.
var ErrBudget = errors.New("the run reached its cost budget")

// All models in a budgeted delegation tree share one account. A model
// call reserves the remaining budget until its usage is known, so two
// children cannot each spend the same remainder. Tools still run in
// parallel; model calls without a ceiling are unaffected.
type sharedBudget struct {
	owner *Agent
	slot  chan struct{}
	mu    sync.Mutex
	spent float64
	known bool
}

func (a *Agent) sharedAccount() *sharedBudget {
	a.budgetMu.Lock()
	defer a.budgetMu.Unlock()
	if a.budget == nil && a.MaxCost() > 0 {
		spent, _, _, known := a.SessionCost()
		a.budget = &sharedBudget{owner: a, slot: make(chan struct{}, 1), spent: spent, known: known}
	}
	return a.budget
}

// stream is the admission and billing point for main and auxiliary
// model calls alike. Usage is charged here exactly once, independently
// of when a child's transcript is later billed to its parent.
func (a *Agent) stream(ctx context.Context, p llm.Provider, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if err := a.persistenceErr(); err != nil {
		return nil, err
	}
	id := ""
	if sess, ok := a.Log.(*session.Session); ok {
		id = sess.Meta.ID
	}
	if id == "" {
		a.usageMu.Lock()
		if a.requestSessionID == "" {
			a.requestSessionID = rand.Text()
		}
		id = a.requestSessionID
		a.usageMu.Unlock()
	}
	ctx = llm.WithSessionID(ctx, id)
	// The persistent cap is asked before any call, whichever agent makes
	// it: this is the one place main, routed, advisor, side and subagent
	// calls all pass. It is the same refusal --max-cost gives, so the
	// headless runner exits 55 for it too.
	if err := a.Spend.Check(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBudget, err)
	}
	resp, err := a.streamAccounted(ctx, p, req, emit)
	a.chargeSpend(req.Model, resp)
	return resp, err
}

// chargeSpend adds a finished call to the persistent ledger. Only a
// reported or priced usage is charged: a call that failed before the
// provider billed anything costs nothing, and an unpriced model cannot be
// measured against a cap. A ledger that cannot be written is logged and
// never stops the call, since the cap is a guard and not a dependency.
func (a *Agent) chargeSpend(model string, resp *llm.Response) {
	if a.Spend == nil || resp == nil {
		return
	}
	cost, ok := resp.Usage.Cost(a.PriceOf(model))
	if !ok {
		return
	}
	if err := a.Spend.Add(cost); err != nil {
		a.debugf("spend ledger: %v", err)
	}
}

// streamAccounted is stream's call to the provider under the run's own
// --max-cost account.
func (a *Agent) streamAccounted(ctx context.Context, p llm.Provider, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	b := a.sharedAccount()
	if b == nil {
		return p.Stream(ctx, req, emit)
	}
	if b.owner.MaxCost() > 0 {
		select {
		case b.slot <- struct{}{}:
			defer func() { <-b.slot }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	spent, known := b.spent, b.known
	b.mu.Unlock()
	ceiling := b.owner.MaxCost()
	if ceiling > 0 && known && spent >= ceiling {
		return nil, fmt.Errorf("%w: spent %s of %s", ErrBudget, llm.FormatUSDPrecise(spent), llm.FormatUSDPrecise(ceiling))
	}
	var out, thinking strings.Builder
	produced := false
	resp, err := p.Stream(ctx, req, func(e llm.Event) {
		produced = produced || isTokenDelta(e)
		switch e.Type {
		case llm.EventTextDelta, llm.EventToolUseDelta:
			out.WriteString(e.Text)
		case llm.EventThinkingDelta:
			thinking.WriteString(e.Text)
		}
		if emit != nil {
			emit(e)
		}
	})
	usage := streamedUsage(req, resp, err, produced, out.String(), thinking.String())
	if cost, ok := usage.Cost(a.PriceOf(req.Model)); ok {
		b.mu.Lock()
		b.spent += cost
		b.known = true
		b.mu.Unlock()
	}
	return resp, err
}

// streamedUsage uses provider figures when present. A failed stream that
// generated tokens but ended before its usage frame is estimated from the
// request and generated output. A failure before generation is not billed
// by guesswork. Both the session and shared budget use this calculation;
// updating the session totals must not charge the shared account again.
func streamedUsage(req llm.Request, resp *llm.Response, err error, produced bool, out, thinking string) llm.Usage {
	if resp != nil && resp.Usage != (llm.Usage{}) {
		return resp.Usage
	}
	if err == nil || !produced {
		return llm.Usage{}
	}
	input := estimateTokens(req.System)
	for _, m := range req.Messages {
		input += messageTokens(m)
	}
	for _, d := range req.Tools {
		input += estimateTokens(d.Description) + estimateTokens(string(d.InputSchema))
	}
	return cancelledUsage(input, out, thinking)
}

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
	if b := a.sharedAccount(); b != nil {
		ceiling := b.owner.MaxCost()
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.spent, ceiling > 0 && b.known && b.spent >= ceiling
	}
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
