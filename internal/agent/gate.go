package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
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
	if !a.Verify.Enabled() || a.mutations == 0 || a.mutations == g.seen {
		return false
	}
	g.seen = a.mutations
	emit(Event{Kind: EvNotice, Text: "verifying: " + strings.Join(a.Verify.Config.Commands, " · ")})
	res := a.Verify.Run(ctx)
	if ctx.Err() != nil {
		return false
	}
	a.working.Verify = res.Summary()
	if res.Mutation != nil && res.Mutation.Skipped == "" {
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
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("%s · sending the model back to fix it (%d/%d)", res.Summary(), g.rounds, verify.MaxRounds)})
	a.appendMessage(llm.HarnessMessage(llm.RoleUser, res.Feedback()))
	return true
}

// stopHook gives stop hooks a chance to send the model back to work. It
// reports whether the turn continues.
func (a *Agent) stopHook(ctx context.Context, text string, stops *int, emit func(Event)) bool {
	if !a.Hooks.Has(hook.Stop) || *stops >= maxStopContinues {
		return false
	}
	res := a.Hooks.Run(ctx, hook.Input{Event: hook.Stop, Cwd: a.Hooks.Cwd, Text: text})
	if res.Continue == "" {
		return false
	}
	*stops++
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("stop hook sent the model back to work (%d/%d): %s", *stops, maxStopContinues, res.Continue)})
	a.appendMessage(llm.HarnessMessage(llm.RoleUser, "[A stop hook did not accept this as done]\n"+res.Continue))
	return true
}
