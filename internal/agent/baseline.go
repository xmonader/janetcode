package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

// The pre-edit baseline (VerifyBaseline): the project's checks run at the
// start of a turn, in the background, on the tree as it was before the
// turn changed anything. The first change waits for it — that wait is
// what makes it a baseline rather than a snapshot copy of the tree — and
// at the end of the turn the gate sends the model back only for what the
// turn made worse. A repository whose tests were already red no longer
// costs a repair round per failure the agent did not cause.
//
// A turn that never changes anything never waits: the run is cancelled
// when the turn ends. A baseline that does not finish within
// baselineMaxWait is dropped and the gate behaves exactly as without it.

// baselineMaxWait bounds how long the first change waits for the
// baseline. Past it the run is cancelled and the turn carries on without
// one: a slow suite must not hold the turn's first edit for its whole
// timeout.
var baselineMaxWait = 3 * time.Minute

// baselineRun is one turn's baseline.
type baselineRun struct {
	done   chan struct{}
	cancel context.CancelFunc
	rep    verify.Report
	// usable is set once the run finished on the unchanged tree; a
	// cancelled or abandoned run is never compared against.
	usable bool
	waited bool
}

// SetVerifyBaseline turns the pre-edit baseline on or off for the turns
// that follow.
func (a *Agent) SetVerifyBaseline(on bool) {
	a.featMu.Lock()
	a.VerifyBaseline = on
	a.featMu.Unlock()
}

func (a *Agent) verifyBaselineOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.VerifyBaseline
}

// startBaseline starts the turn's baseline run when it is wanted and can
// mean anything: a gate with commands, a session that may change files,
// and no mutation testing (the full run does not do it, and a baseline
// that silently skipped it would change what the gate checks).
func (a *Agent) startBaseline(ctx context.Context) {
	a.baseline = nil
	if !a.verifyBaselineOn() || a.Off["baseline"] || a.Off["verify"] || a.readOnly() || a.DryRunOn() {
		return
	}
	gate, _ := a.VerifyGate()
	if !gate.Enabled() || len(gate.Config.Commands) == 0 || strings.TrimSpace(gate.Config.Mutate) != "" {
		return
	}
	bctx, cancel := context.WithCancel(ctx)
	run := &baselineRun{done: make(chan struct{}), cancel: cancel}
	a.baseline = run
	go func() {
		defer close(run.done)
		rep := gate.RunEach(bctx)
		if bctx.Err() == nil && len(rep.Commands) == len(gate.Config.Commands) {
			run.rep, run.usable = rep, true
		}
	}()
}

// awaitBaseline is called before a call that changes something. The
// first such call of the turn waits for the baseline to finish, so it saw
// the tree before the change; later calls find it done.
func (a *Agent) awaitBaseline(ctx context.Context, emit func(Event)) {
	run := a.baseline
	if run == nil || run.waited {
		return
	}
	run.waited = true
	select {
	case <-run.done:
		return
	default:
	}
	if emit != nil {
		emit(Event{Kind: EvNotice, Text: "waiting for the pre-edit check run to finish before the first change"})
	}
	t := time.NewTimer(baselineMaxWait)
	defer t.Stop()
	select {
	case <-run.done:
	case <-t.C:
		run.cancel()
		<-run.done
		run.usable = false
		if emit != nil {
			emit(Event{Kind: EvNotice, Text: fmt.Sprintf("pre-edit check run took longer than %s; continuing without a baseline", baselineMaxWait)})
		}
	case <-ctx.Done():
		run.cancel()
		<-run.done
		run.usable = false
	}
}

// endBaseline stops a baseline the turn never needed.
func (a *Agent) endBaseline() {
	if run := a.baseline; run != nil {
		run.cancel()
		a.baseline = nil
	}
}

// usableBaseline is the turn's baseline when the gate can compare
// against it: it finished, on the tree as it was before the first change.
func (a *Agent) usableBaseline() *baselineRun {
	run := a.baseline
	if run == nil || !run.waited {
		return nil
	}
	<-run.done
	if !run.usable {
		return nil
	}
	return run
}

// baselineGate is the gate's run when a baseline exists: every command,
// compared with the run before the turn. It reports the result to log
// and the feedback for the model, empty when the turn is not to be sent
// back.
//
// What the turn broke goes back on the gate's usual rounds. Failures that
// were all there before the turn go back once, as a reminder rather than
// a repair order: the request may well be "make these pass", and a gate
// that never mentioned them again would let a half-fixed turn end
// silently; a turn that has nothing to do with them is told it may
// finish, and is not sent back again for them.
func (a *Agent) baselineGate(ctx context.Context, g *gateState, gate *verify.Runner, run *baselineRun, emit func(Event)) (res verify.Result, feedback string) {
	rep := gate.RunEach(ctx)
	res = rep.Result()
	if rep.OK() {
		return res, ""
	}
	d := verify.Compare(run.rep, rep)
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "verify/baseline", Output: d.Summary()})
	if d.Regressed() {
		return res, d.Feedback()
	}
	if g.preNoted {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("%s · %s · not caused by this turn, so not sent back again", res.Summary(), d.Summary())})
		return res, ""
	}
	g.preNoted = true
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("%s · %s · reminding the model once", res.Summary(), d.Summary())})
	return res, d.Reminder()
}
