package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

// maxStopContinues bounds how often stop hooks may extend one turn.
const maxStopContinues = 3

// gateState tracks verification within one turn.
type gateState struct {
	rounds int
	seen   int // a.mutations when the checks last ran
}

// verifyGate runs the project's verification commands when the model ends
// a turn after changing something. A failure is fed back and the turn
// continues; a pass is reported. Checks rerun only after further changes,
// and at most verify.MaxRounds failures are sent back.
func (a *Agent) verifyGate(ctx context.Context, g *gateState, emit func(Event)) bool {
	gate, _ := a.VerifyGate()
	if !gate.Enabled() || a.mutations == 0 || a.mutations == g.seen {
		return false
	}
	// While /accept tests are being written or await approval they are
	// meant to fail; a project check running them would send the model
	// back to "fix" the one state the phase is supposed to end in.
	if phase, on := a.acceptPhase(); on && (phase == session.AcceptWriting || phase == session.AcceptProposed) {
		return false
	}
	g.seen = a.mutations
	emit(Event{Kind: EvNotice, Text: "verifying: " + strings.Join(gate.Config.Commands, " · ")})
	res := gate.Run(ctx)
	if ctx.Err() != nil {
		return false
	}
	a.logGate(session.GateVerify, res)
	a.working.Verify = res.Summary()
	if res.Mutation != nil && res.Mutation.Scored() {
		a.logEvent(res.Mutation.Event())
	}
	if res.OK() {
		emit(Event{Kind: EvNotice, Text: res.Summary()})
		return false
	}
	g.rounds++
	if g.rounds > verify.MaxRounds {
		emit(Event{Kind: EvNotice, Text: res.Summary() + " · giving up after " + fmt.Sprint(verify.MaxRounds) + " rounds"})
		return false
	}
	// The optional decision provider gets a say before the round is
	// spent: a confident environmental/flaky/unrelated verdict ends the
	// turn without spending a model call on it. The round is counted
	// either way, so the worst case of a wrong answer is one round
	// fewer, never a loop.
	if a.triageFailure(ctx, res, emit) {
		emit(Event{Kind: EvNotice, Text: res.Summary() + " · repair round not spent (" + fmt.Sprint(g.rounds) + "/" + fmt.Sprint(verify.MaxRounds) + ")"})
		return false
	}
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("%s · sending the model back to fix it (%d/%d)", res.Summary(), g.rounds, verify.MaxRounds)})
	a.appendHarnessText(res.Feedback())
	return true
}

// logGate records each command a gate ran as a session.TypeGate event,
// so the verdict fold (see FoldOutcomeIn) counts a gate's runs as checks
// the turn ran. A passing command's output is not kept by verify.Runner,
// so only a failure carries one.
func (a *Agent) logGate(source string, res verify.Result) {
	for _, c := range res.Passed {
		a.logEvent(session.Event{Type: session.TypeGate, Turn: a.turnN, Name: c, Source: source})
	}
	if res.Failed != "" {
		a.logEvent(session.Event{Type: session.TypeGate, Turn: a.turnN, Name: res.Failed, Output: res.Output, IsError: true, Source: source})
	}
}

// stopHook gives stop hooks a chance to send the model back to work. It
// reports whether the turn continues.
func (a *Agent) stopHook(ctx context.Context, text string, stops *int, emit func(Event)) bool {
	hooks := a.HookRunner()
	if !hooks.Has(hook.Stop) || *stops >= maxStopContinues {
		return false
	}
	res := hooks.Run(ctx, hook.Input{Event: hook.Stop, Cwd: hooks.Cwd, Text: text})
	if res.Continue == "" {
		return false
	}
	*stops++
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("stop hook sent the model back to work (%d/%d): %s", *stops, maxStopContinues, res.Continue)})
	a.appendHarnessText("[A stop hook did not accept this as done]\n" + res.Continue)
	return true
}
