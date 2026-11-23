package agent

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// The forced advisor: a consult with a stronger model whose answer is
// guidance TEXT injected into the executor's context. The advisor does
// not act, has no tools and cannot edit anything -- Advise already has
// those properties, and this file only decides when it is called.
//
// It is forced by the harness rather than offered to the model, because
// a consult offered as a tool is a consult the model declines. In the
// mixai measurements the two strategies that put the panel behind a tool
// call -- "fusion" and the advisor tool itself -- were never pressed, and
// scored identically to the cheap model working alone. So the choice is
// not the model's.
//
// AdvisorAlways is deliberately measured before any gating. If forcing a
// consult on every turn does not raise the pass rate, then no policy that
// fires it *less* often can: gating can only ever recover a fraction of a
// gain that has to exist first.

// Advisor modes for AdvisorMode. Off is the default and is
// byte-identical to the feature not existing.
const (
	AdvisorOff    = "off"
	AdvisorAlways = "always"
	// AdvisorTriggered consults only on a failure event (see
	// advisortrigger.go). Experimental and unmeasured.
	AdvisorTriggered = "triggered"
)

// advisorForcedCap is how many forced consults one turn may buy when
// AdvisorMaxConsults is unset. Two: one at the start, one when the gate
// first says the work is wrong.
const advisorForcedCap = 2

// advisorForcedTimeout bounds one consult. Advice that arrives after the
// executor has moved on is not advice, and the turn continues without it.
const advisorForcedTimeout = 45 * time.Second

// advisorForcedEnabled reports whether the harness forces consults.
func (a *Agent) advisorForcedEnabled() bool {
	if a.Off["advisor"] {
		return false
	}
	mode, _ := a.advisorForcedSettings()
	return mode == AdvisorAlways
}

// advisorForcedBudget is how many forced consults are left this turn.
func (a *Agent) advisorForcedBudget() int {
	_, cap := a.advisorForcedSettings()
	if cap <= 0 {
		cap = advisorForcedCap
	}
	return cap - int(atomic.LoadInt32(&a.advisorForced))
}

// forceAdvice buys one consult and appends the answer to the executor's
// context as ordinary harness text. It fails open in every direction: a
// spent budget, a provider error, a timeout, an empty reply and a
// cancelled turn all leave the conversation exactly as it was. why names
// the trigger, for the session log and for counting which triggers paid.
func (a *Agent) forceAdvice(ctx context.Context, why, question string, emit func(Event)) bool {
	if !a.advisorForcedEnabled() {
		return false
	}
	return a.consult(ctx, why, question, emit)
}

// consult is forceAdvice past the mode check, shared with the triggered
// mode so both buy the same consult under the same cap and timeout.
func (a *Agent) consult(ctx context.Context, why, question string, emit func(Event)) bool {
	if ctx.Err() != nil {
		return false
	}
	if a.advisorForcedBudget() <= 0 {
		return false
	}
	atomic.AddInt32(&a.advisorForced, 1)
	cctx, cancel := context.WithTimeout(ctx, advisorForcedTimeout)
	defer cancel()
	text, err := a.Advise(cctx, question)
	if err != nil || strings.TrimSpace(text) == "" {
		// Fail open: no advice is today's behaviour, not a failed turn.
		if emit != nil && err != nil && ctx.Err() == nil {
			emit(Event{Kind: EvNotice, Text: "advisor consult (" + why + ") failed; continuing without it"})
		}
		return false
	}
	a.stats.advisorConsult()
	if emit != nil {
		emit(Event{Kind: EvNotice, Text: "advisor consulted (" + why + ")"})
	}
	a.appendHarnessText(fmt.Sprintf("Guidance from a consulting model (it has no tools and did not act; treat it as advice, not as fact):\n%s", text))
	return true
}

// adviseAtTurnStart is the first of the two forced triggers. In the
// orient style it is the orient consult instead (orient.go), which runs
// whether or not the advisor is forced.
func (a *Agent) adviseAtTurnStart(ctx context.Context, request string, emit func(Event)) {
	if strings.TrimSpace(request) == "" {
		return
	}
	if a.turnStartStyle() == TurnStartOrient {
		a.orientAtTurnStart(ctx, request, emit)
		return
	}
	a.planAtTurnStart(ctx, request, emit)
}

// planAtTurnStart is the original turn-start consult. Unlike the other
// forced consult, a reply the token limit cut off, or one that is only
// thinking, is dropped rather than injected (see turnStartReply).
func (a *Agent) planAtTurnStart(ctx context.Context, request string, emit func(Event)) bool {
	if !a.advisorForcedEnabled() || ctx.Err() != nil || a.advisorForcedBudget() <= 0 {
		return false
	}
	atomic.AddInt32(&a.advisorForced, 1)
	cctx, cancel := context.WithTimeout(ctx, advisorForcedTimeout)
	defer cancel()
	r, err := a.sideQueryReply(cctx, advisorSystem, "The executor is about to start on this request:\n\n"+request+
		"\n\nGive it the plan: the concrete next steps, the pitfalls this particular task has, and how to verify it is right.", a.AdvisorModelName(), turnStartMaxTokens)
	text, ok := turnStartReply(ctx, r, err, "advisor consult (turn start)", emit)
	if !ok {
		return false
	}
	a.stats.advisorConsult()
	if emit != nil {
		emit(Event{Kind: EvNotice, Text: "advisor consulted (turn start)"})
	}
	a.appendHarnessText(fmt.Sprintf("Guidance from a consulting model (it has no tools and did not act; treat it as advice, not as fact):\n[advisor %s]\n%s", r.Model, text))
	return true
}

// adviseOnGateFailure is the second, and the one with a real signal
// behind it: the project's own checks have said the work is wrong.
func (a *Agent) adviseOnGateFailure(ctx context.Context, feedback string, emit func(Event)) {
	a.forceAdvice(ctx, "gate failure",
		"The executor's change failed the project's own verification:\n\n"+feedback+
			"\n\nSay what is most likely wrong and what to change, concretely.", emit)
}
