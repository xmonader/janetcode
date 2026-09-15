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

// call sends the conversation to the session's model, or, when the
// step-routing policy allows it, to RouteCheap instead (see steproute.go).
// A routed call that fails outright falls back to Model for this one call
// rather than being treated as a policy question; Model's own failures
// still fall back to the configured alternatives when the failure is one
// another model could survive (see fallback).
func (a *Agent) call(ctx context.Context, emit func(Event)) (*llm.Response, error) {
	s := a.Settings()
	if prov, model, ok := a.routeStep(emit); ok {
		resp, err := a.callOn(ctx, prov, model, emit)
		if err == nil {
			a.stats.routed()
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		emit(Event{Kind: EvNotice, Text: "routed call to " + model + " failed (" + textfmt.FirstLine(err.Error(), 70) + "); this step goes to " + s.Model})
		a.stepRoute.taint("a routed call failed")
	}
	resp, err := a.callOn(ctx, s.Provider, s.Model, emit)
	if err == nil {
		return resp, nil
	}
	return a.fallback(ctx, err, emit)
}

func (a *Agent) callOn(ctx context.Context, prov llm.Provider, model string, emit func(Event)) (*llm.Response, error) {
	start := time.Now()
	a.debugf("call start: %d messages, ~%d tokens", len(a.Messages), a.EstimateTokens())
	system := a.systemPrompt()
	defs := a.turnTools()
	a.usageMu.Lock()
	a.prefix.note(a.turnN, system, a.Messages, defs)
	a.usageMu.Unlock()
	resp, err := prov.Stream(ctx, llm.Request{
		Model:     model,
		System:    system,
		Messages:  a.Messages,
		Tools:     defs,
		MaxTokens: a.Settings().MaxTokens,
		Effort:    a.Settings().Effort,
	}, func(e llm.Event) {
		if e.Type == llm.EventRetried && e.Retry != nil {
			a.stats.retried(e.Retry.Attempts)
		}
		forward(e, emit)
	})
	var u llm.Usage
	if resp != nil {
		u = resp.Usage
	}
	a.stats.model(model, time.Since(start), u, err != nil)
	if err == nil {
		a.lastCallModel = model
	}
	if errors.Is(err, llm.ErrStreamWatchdog) {
		a.stats.watchdogKill()
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
	if a.tools != nil {
		return a.tools
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
	msgs := append(a.MessagesSnapshot(), llm.TextMessage(llm.RoleUser, question))
	max := s.MaxTokens
	if max <= 0 || max > 2048 {
		max = 2048
	}
	resp, err := s.Provider.Stream(ctx, llm.Request{
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
