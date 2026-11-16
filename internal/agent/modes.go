package agent

import (
	"encoding/json"
	"fmt"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// This file is the one place the session modes — read-only, plan mode and
// the /accept phases — decide what the model may do: which calls are
// refused before they reach authorize, what the system prompt says about
// the mode, and whether step routing may run. Approval shortcuts
// (AutoApprove, AutoEdits, AutoMode) are not refusals and stay in
// authorize.

const readOnlySystem = "\n\n# Read-only\nThis session is read-only: you can read, search and run read-only tools, but nothing that changes files or runs commands is available, and asking for it will fail. Do not pretend a change was made."

const planSystem = "\n\n# Plan mode\nThe user wants a plan before any change. Explore the code as needed, then write a concrete, numbered plan: which files change and how, in what order, how each step is verified, and what could go wrong. Put it under a heading `## Plan`. Then stop: approval is the user typing `/plan approve`, which is the only thing that leaves plan mode. Do not use the ask tool to ask whether to start — no answer to it can start anything, and offering \"go ahead\" as a choice promises something you cannot do. Use ask only for a real choice inside the plan, one that changes what the plan says."

// readOnly reports whether mutating tools are hidden and refused: in
// read-only mode and in plan mode.
func (a *Agent) readOnly() bool { return a.Settings().readOnly() }

// toolGate is the mode check a tool call must pass before authorize runs:
// read-only and plan mode refuse every mutating tool, and the /accept
// writing and proposed phases refuse every mutating tool except a test
// file edit (see accept.Mode.WritingDenial). It reports the refusal the model
// sees and whether the call is denied.
//
// executeTool (the model's own calls) and runNested (calls made from a
// script or a recipe) both call it, so a script is never a way around a
// mode. A locked acceptance test is refused inside authorize instead
// (accept.Mode.Denial), after the pre-tool hook, because that check has to see
// the input a hook may have rewritten.
func (a *Agent) toolGate(t tool.Tool, input json.RawMessage) (reason string, denied bool) {
	if !tool.MutatingCall(t, input) {
		return "", false
	}
	if s := a.Settings(); s.readOnly() {
		return fmt.Sprintf("%s is not available: this session is read-only (%s). Describe what you would do instead.", t.Name(), s.modeName()), true
	}
	if reason := a.admitGate(t, input); reason != "" {
		return reason, true
	}
	if reason := a.locks().WritingDenial(t, input); reason != "" {
		return reason, true
	}
	return "", false
}

// modeSystem is what the system prompt says about the active modes, or ""
// when none is on.
func (a *Agent) modeSystem() string {
	text := ""
	if s := a.Settings(); s.readOnly() {
		text += readOnlySystem
		if s.PlanMode {
			text += planSystem
		}
	}
	text += a.admitSystem()
	if !a.Off["accept"] {
		text += a.accept.Load().SystemText()
	}
	if a.DryRunOn() {
		text += dryRunSystem
	}
	return text
}

// routingAllowed reports whether the active modes let a step be routed to
// RouteCheap at all: plan mode and the /accept writing phase are the
// deciding part of a task, so every call in them stays on Model.
func (a *Agent) routingAllowed() bool {
	if a.Settings().PlanMode {
		return false
	}
	if phase, on := a.acceptPhase(); on && phase == session.AcceptWriting {
		return false
	}
	return true
}

// refuseCall reports a tool call the harness refused without running it:
// an EvToolResult for the front-end and a tool result on the session log.
// A call that never runs still has to be written down — a refusal is the
// harness deciding something, and without the log entry neither /why nor
// anything else reading the session afterwards could tell a call the user
// refused from one that was never made. denied says whether the refusal
// is a permission decision (a rule, a mode, the user) rather than a
// malformed call, so the headless recorder can count it as one.
func (a *Agent) refuseCall(call *ToolCall, msg string, denied bool, emit func(Event)) {
	emit(Event{Kind: EvToolResult, Call: call, Output: msg, IsError: true, Denied: denied})
	a.logEvent(session.Event{Type: session.TypeToolResult, Turn: a.turnN, Step: a.stepN,
		CallID: call.ID, Name: call.Name, Arguments: string(call.Input), Output: msg, IsError: true, Denied: denied})
}
