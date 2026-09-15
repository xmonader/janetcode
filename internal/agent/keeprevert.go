package agent

import (
	"context"
	"fmt"
	"strings"
)

// After a goal round that made the tree worse, put the files back and
// tell the model why, once. The user can already /undo; this joins the
// verdict to that checkpoint so a turn that fails its own check does not
// stand. Off["keeprevert"] restores the previous behaviour: the turn
// stays, the next round starts on top of it.

const maxKeepReverts = 2

// keepOrRevert compares this turn's outcome to the previous goal round
// and, on a regression, restores the turn's files and asks the model to
// try once more with that evidence. It reports whether the turn should
// continue.
func (a *Agent) keepOrRevert(ctx context.Context, outcome TurnOutcome, emit func(Event)) bool {
	if a.Off["keeprevert"] || a.Log == nil {
		return false
	}
	if ActiveGoal(a.Log.Events()) == nil {
		return false
	}
	if a.steeredThisTurn {
		a.noteGoalOutcome(outcome)
		return false
	}
	if !a.goalOutSet || !regress(a.lastGoalOut, outcome) {
		a.keepReverts = 0
		a.noteGoalOutcome(outcome)
		return false
	}
	if a.keepReverts >= maxKeepReverts {
		a.noteGoalOutcome(outcome)
		return false
	}
	done, err := a.revertLastTurnFiles(ctx)
	if err != nil {
		return false
	}
	a.keepReverts++
	why := "this round made things worse (" + string(outcome.Verdict) + ": " + outcome.Why + ") than the previous one (" + string(a.lastGoalOut.Verdict) + ")"
	if len(done) > 0 {
		why += "; restored " + strings.Join(done, ", ")
	}
	emit(Event{Kind: EvNotice, Text: "keep-or-revert: " + why})
	a.appendHarnessText("[harness] " + why + ". The files this turn changed have been put back. Try a different approach; do not redo the same edit.")
	return true
}

func (a *Agent) noteGoalOutcome(o TurnOutcome) {
	a.lastGoalOut, a.goalOutSet = o, true
}

// regress reports whether cur is a step backwards from prev: a check
// that used to be green is now red, or verified work became a failed
// check. A turn that changed nothing is not a regression to revert.
func regress(prev, cur TurnOutcome) bool {
	if cur.Edited == 0 {
		return false
	}
	if prev.OK() && cur.Verdict == VerdictCheckFailed {
		return true
	}
	if prev.Verdict == VerdictVerified && cur.Verdict == VerdictCheckFailed {
		return true
	}
	return false
}

// revertLastTurnFiles puts the work tree back the way it was before this
// turn's edits, and leaves the conversation alone so the model can see
// what it did and why it was undone.
func (a *Agent) revertLastTurnFiles(ctx context.Context) ([]string, error) {
	a.cp.mu.Lock()
	defer a.cp.mu.Unlock()
	n := len(a.cp.all)
	if n == 0 {
		return nil, ErrNothingToUndo
	}
	cp := a.cp.all[n-1]
	if cp.Turn != a.turnN && cp.Turn != 0 {
		// The last checkpoint is not this turn: nothing of ours to put back.
		return nil, fmt.Errorf("no checkpoint for turn %d", a.turnN)
	}
	a.cp.all = a.cp.all[:n-1]
	a.cp.open = false
	a.cp.dirty = true
	done, err := a.restore(ctx, cp)
	a.cp.mu.Unlock()
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	a.cp.mu.Lock()
	return done, err
}
