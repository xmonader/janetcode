package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// callRoute is the routing evidence for one callOn: which slot the
// model came from ("main" for the session's own), what kind of detour
// it was — "route" for a cheap step, "escalate" for the failure
// cascade, "fallback" after a provider failure, "" for no detour — and
// the reason in the policy's own words. callOn stamps it on the agent
// beside lastCallModel when the call succeeds, and recordReply logs it
// on the assistant event (see session.Event.Slot and friends).
type callRoute struct {
	slot string
	kind string
	why  string
}

// call sends the conversation to the session's model, or, when the
// step-routing policy allows it, to RouteCheap instead (see steproute.go).
// A routed call that fails outright falls back to Model for this one call
// rather than being treated as a policy question; Model's own failures
// still fall back to the configured alternatives when the failure is one
// another model could survive (see fallback).
func (a *Agent) call(ctx context.Context, emit func(Event)) (*llm.Response, error) {
	s := a.Settings()
	// A turn that has already failed its own verification has no business
	// dipping to a cheaper tier, so the cascade is asked first.
	if prov, model, ok := a.escalateStep(emit); ok {
		why, _ := a.escalated()
		streamed, track := streamedAny(emit)
		resp, err := a.callOn(ctx, prov, model, callRoute{slot: "strong", kind: "escalate", why: why}, track)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil || errors.Is(err, ErrBudget) {
			return nil, err
		}
		// What the failed call streamed is void, as before a fallback:
		// Model's reply replaces it rather than running on from it. A
		// call refused before it said anything has nothing to take back,
		// and a discard would tell the front-ends an answer was cut off.
		if *streamed {
			emit(Event{Kind: EvDiscard})
		}
		emit(Event{Kind: EvNotice, Text: "escalated call to " + model + " failed (" + textfmt.FirstLine(err.Error(), 70) + "); this step goes to " + s.Model})
	} else if prov, model, why, ok := a.routeStep(emit); ok {
		streamed, track := streamedAny(emit)
		resp, err := a.callOn(ctx, prov, model, callRoute{slot: "cheap", kind: "route", why: why}, track)
		if err == nil {
			// Apply the same restriction to gateways that return tools as
			// prose, and to providers that ignore the advertised tool list.
			a.recoverToolCalls(resp, func(Event) {})
			for _, u := range resp.Message.ToolUses() {
				if !mechanicalTools[u.Name] {
					a.addUsage(model, resp.Usage)
					err = fmt.Errorf("cheap routing is limited to repository reads; %s requires the main model", u.Name)
					break
				}
			}
		}
		if err == nil {
			a.stats.routed()
			return resp, nil
		}
		if ctx.Err() != nil || errors.Is(err, ErrBudget) {
			return nil, err
		}
		if *streamed {
			emit(Event{Kind: EvDiscard})
		}
		emit(Event{Kind: EvNotice, Text: "routed call to " + model + " failed (" + textfmt.FirstLine(err.Error(), 70) + "); this step goes to " + s.Model})
		a.stepRoute.taint("a routed call failed")
	}
	resp, err := a.callOn(ctx, s.Provider, s.Model, callRoute{slot: "main"}, emit)
	if err == nil {
		return resp, nil
	}
	return a.fallback(ctx, err, emit)
}

// streamedAny wraps emit to note whether the call put any part of an
// answer in front of the user: text, thinking or a tool call. Whether a
// failed call's output needs discarding depends on it.
func streamedAny(emit func(Event)) (*bool, func(Event)) {
	var streamed bool
	return &streamed, func(e Event) {
		switch e.Kind {
		case EvText, EvThinking:
			streamed = streamed || e.Text != ""
		case EvToolStart, EvToolInput:
			streamed = true
		}
		emit(e)
	}
}

// isTokenDelta reports whether a stream event is the provider actually
// producing output. A status or a retry notice is the harness talking,
// not the model, and counting one as the first token would report a
// queued call as instant.
func isTokenDelta(e llm.Event) bool {
	switch e.Type {
	case llm.EventTextDelta, llm.EventThinkingDelta:
		return e.Text != ""
	case llm.EventToolUseStart, llm.EventToolUseDelta:
		return true
	}
	return false
}

func (a *Agent) callOn(ctx context.Context, prov llm.Provider, model string, rt callRoute, emit func(Event)) (*llm.Response, error) {
	start := time.Now()
	reqTokens := a.EstimateTokens()
	a.debugf("call start: %d messages, ~%d tokens", len(a.Messages), reqTokens)
	// Keep the generated output for usage estimates when a stream ends
	// before its usage frame, whether cancelled or failed by the provider.
	var streamedOut, streamedThink strings.Builder
	// firstToken is when the provider produced anything at all. A call
	// that never streams a token (a non-streaming provider, a failure)
	// leaves it zero, and the split is reported as unknown rather than
	// as instantaneous.
	var firstToken time.Time
	system := a.systemPrompt()
	defs := a.turnTools()
	if rt.kind == "route" {
		defs = append([]llm.ToolDef(nil), defs...)
		kept := defs[:0]
		for _, d := range defs {
			if mechanicalTools[d.Name] {
				kept = append(kept, d)
			}
		}
		defs = kept
	}
	a.usageMu.Lock()
	a.prefix.note(a.turnN, system, a.Messages, defs)
	a.usageMu.Unlock()
	plan := a.newPlanWatch()
	burst := a.newBurstWatch()
	effort, steered := a.callEffort(prov, emit)
	callCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	sent := a.bridgeImages(ctx, a.Messages, model, emit)
	a.logRequest(prov, model, system, defs, a.Messages, sent, effort)
	req := llm.Request{
		Model:     model,
		System:    system,
		Messages:  sent,
		Tools:     defs,
		MaxTokens: a.maxTokens(),
		Effort:    effort,
		// Off, the plan a reply reasoned out is gone by the next reply
		// on the OpenAI-compatible path; see llm.Block.Via.
		DropReasoning:  !a.replayOpenAI(),
		ReasoningChars: a.ReasoningChars,
		ReasoningLast:  a.ReasoningLast,
	}
	resp, err := a.stream(callCtx, prov, req, func(e llm.Event) {
		if e.Type == llm.EventRetried && e.Retry != nil {
			a.stats.retried(e.Retry.Attempts)
		}
		if firstToken.IsZero() && isTokenDelta(e) {
			firstToken = time.Now()
		}
		switch e.Type {
		case llm.EventTextDelta, llm.EventToolUseDelta:
			streamedOut.WriteString(e.Text)
		case llm.EventThinkingDelta:
			streamedThink.WriteString(e.Text)
		}
		forward(e, emit)
		if plan.note(e) {
			cancel(errPlanBudget)
		}
		if burst.note(e) {
			cancel(errBurstCap)
		}
	})
	// Capture the original call's spend before a harness cut replaces its
	// response. The synthetic reply will carry it into recordReply once.
	u := streamedUsage(req, resp, err, !firstToken.IsZero(), streamedOut.String(), streamedThink.String())
	if ctx.Err() == nil && planBudgetCut(callCtx, err) {
		a.planCuts++
		a.planCutNow = true
		resp, err = plan.cutReply(model), nil
		resp.Usage = u
	}
	if ctx.Err() == nil && err != nil && errors.Is(context.Cause(callCtx), errBurstCap) {
		resp, err = burst.reply(model), nil
		resp.Usage = u
		a.burstRan, a.burstRepeat = len(resp.Message.ToolUses()), burst.repeat
	}
	took := time.Since(start)
	a.lastTTFT, a.lastDecode = 0, 0
	if !firstToken.IsZero() {
		a.lastTTFT = firstToken.Sub(start)
		if d := took - a.lastTTFT; d > 0 {
			a.lastDecode = d
		}
	}
	served := ""
	if resp != nil {
		served = resp.Model
	}
	a.stats.model(model, served, took, u, err != nil)
	a.stats.firstToken(model, a.lastTTFT)
	if err == nil {
		a.lastCallModel = model
		a.lastCallSlot = rt.slot
		a.lastCallRoute = rt.kind
		a.lastCallWhy = rt.why
		a.lastCallEffort = ""
		if steered {
			a.lastCallEffort = effort
		}
		a.lastCallProvider = ""
		if prov != nil {
			a.lastCallProvider = prov.Name()
		}
		a.checkServed(model, resp, emit)
	}
	if errors.Is(err, llm.ErrStreamWatchdog) {
		a.stats.watchdogKill()
	}
	// A failed call has no assistant message to carry its spend. Real
	// provider usage wins over estimates, including on cancellation.
	if err != nil && u != (llm.Usage{}) {
		a.addSideUsage(model, u)
	}
	if err != nil {
		a.debugf("call failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	} else {
		a.debugf("call done in %s: stop=%s usage=%+v", time.Since(start).Round(time.Millisecond), resp.StopReason, resp.Usage)
	}
	return resp, err
}

// turnTools is the tool list for this turn, built once and reused for
// every model call in it.
//
// MCP servers connect in the background and register their tools whenever
// they are ready, which can be in the middle of a turn. The tools array is
// part of the cached request prefix, so a tool appearing between two calls
// of the same turn re-bills the whole prompt — and it is not only the
// tools themselves: tool_search names the deferred tools in its own
// description, so even a deferred registration rewrites the array. A tool
// that arrives mid-turn becomes available on the next one, which costs
// nothing anybody notices.
func (a *Agent) turnTools() []llm.ToolDef {
	// A tool the model itself loaded with tool_search joins the list at
	// once: it was told "callable from now on", and a headless -p run is
	// one turn, so waiting for the next turn meant it never got the
	// schema and could not call the tool it had just asked for. This is
	// the one mid-turn change worth the cache re-bill; MCP arrivals are not.
	if act := a.Tools.Activations(); a.tools != nil && act == a.toolsAct {
		return a.tools
	} else {
		a.toolsAct = act
	}
	if a.readOnly() {
		a.tools = a.Tools.DefsIf(func(t tool.Tool) bool { return !t.Mutating() })
	} else {
		a.tools = a.Tools.Defs()
	}
	return a.tools
}

// recoverToolCalls rescues a turn whose tool calls arrived as text. Some
// models — small ones, quantised ones, anything behind a gateway that
// dropped the tool schema — write <invoke name=...> or a <tool_call> JSON
// block into their answer instead of emitting a call. Left alone the turn
// ends having done nothing; parsed, it does what the model asked for.
// Only calls naming a tool this session actually has are taken, so prose
// that merely looks like a call is still prose.
func (a *Agent) recoverToolCalls(resp *llm.Response, emit func(Event)) {
	if resp == nil || resp.StopReason == llm.StopToolUse || len(resp.Message.ToolUses()) > 0 {
		return
	}
	text := resp.Message.Text()
	calls, remaining, ok := llm.RecoverToolCalls(text)
	if !ok {
		return
	}
	kept := make([]llm.RecoveredCall, 0, len(calls))
	for _, c := range calls {
		if _, have := a.Tools.Get(c.Name); have {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return
	}
	blocks := make([]llm.Block, 0, len(resp.Message.Blocks)+len(kept))
	for _, b := range resp.Message.Blocks {
		if b.Type != llm.BlockText {
			blocks = append(blocks, b)
		}
	}
	if remaining != "" {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: remaining})
	}
	blocks = append(blocks, llm.RecoveredBlocks(kept, fmt.Sprintf("recovered_%d_%d", a.turnN, a.stepN))...)
	resp.Message.Blocks = blocks
	resp.StopReason = llm.StopToolUse
	a.stats.recovered(len(kept))
	names := make([]string, 0, len(kept))
	for _, c := range kept {
		names = append(names, c.Name)
	}
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("the model wrote %s as text instead of calling it; recovered and run", strings.Join(names, ", "))})
}

func forward(e llm.Event, emit func(Event)) {
	switch e.Type {
	case llm.EventTextDelta:
		emit(Event{Kind: EvText, Text: e.Text})
	case llm.EventThinkingDelta:
		emit(Event{Kind: EvThinking, Text: e.Text})
	case llm.EventToolUseStart:
		emit(Event{Kind: EvToolStart, Call: &ToolCall{ID: e.Block.ID, Name: e.Block.Name}})
	case llm.EventToolUseDelta:
		emit(Event{Kind: EvToolInput, Text: e.Text})
	case llm.EventStatus:
		emit(Event{Kind: EvPhase, Text: e.Text})
	case llm.EventRetried:
		if e.Retry != nil {
			emit(Event{Kind: EvRetry, Text: retryLine(*e.Retry), Retry: e.Retry})
		}
	case llm.EventStreamDiscard:
		emit(Event{Kind: EvDiscard})
	}
}

// retryLine is the one-liner a completed retry leaves behind.
func retryLine(r llm.Retry) string {
	s := fmt.Sprintf("retried %d×", r.Attempts)
	if r.Reason != "" {
		s += " · " + r.Reason
	}
	if r.Wait > 0 {
		s += fmt.Sprintf(" · waited %s", r.Wait.Round(time.Second/10))
	}
	return s
}

func (a *Agent) debugf(format string, args ...any) {
	if a.Debug == nil {
		return
	}
	fmt.Fprintf(a.Debug, "%s "+format+"\n", append([]any{time.Now().Format("15:04:05.000")}, args...)...)
}

// AskOnce answers a side question against the current conversation
// without recording a user turn or changing Messages.
func (a *Agent) AskOnce(ctx context.Context, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("empty question")
	}
	s := a.Settings()
	if s.Provider == nil {
		return "", fmt.Errorf("no provider")
	}
	// /btw is asked while a turn may be running, so the history is copied
	// under the lock rather than read from under the turn goroutine.
	msgs := sideQuestion(a.MessagesSnapshot(), question)
	max := s.MaxTokens
	if max <= 0 || max > 2048 {
		max = 2048
	}
	resp, err := a.stream(ctx, s.Provider, llm.Request{
		Model:     s.Model,
		System:    a.systemPrompt(),
		Messages:  msgs,
		MaxTokens: max,
		Effort:    s.Effort,
	}, nil)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", fmt.Errorf("empty reply")
	}
	return strings.TrimSpace(resp.Message.Text()), nil
}

// sideQuestion is msgs, a copy the caller owns, with question asked at
// the end of it. A turn in progress can leave the history ending in the
// assistant's tool calls while they run, and a request with a tool_use no
// tool_result answers is refused by every provider: each such call is
// answered as still running. A history ending in a user message takes the
// question into it, the way the next turn merges into one.
func sideQuestion(msgs []llm.Message, question string) []llm.Message {
	if n := len(msgs); n > 0 && msgs[n-1].Role == llm.RoleAssistant {
		if uses := msgs[n-1].ToolUses(); len(uses) > 0 {
			results := make([]llm.Block, 0, len(uses))
			for _, u := range uses {
				results = append(results, llm.ToolResult(replyID(u), "(still running; no result yet)", false))
			}
			msgs = append(msgs, llm.ToolResultMessage(results...))
		}
	}
	q := llm.Block{Type: llm.BlockText, Text: question}
	if n := len(msgs); n > 0 && msgs[n-1].Role == llm.RoleUser {
		msgs[n-1].Blocks = append(msgs[n-1].Blocks, q)
		return msgs
	}
	return append(msgs, llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{q}})
}
