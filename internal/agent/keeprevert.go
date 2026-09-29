package agent

import (
	"context"
	"crypto/sha256"
	"errors"
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
	a.appendHarnessText("[harness] " + why + ". The files this turn's file tools changed have been put back, except any named above as left alone. Try a different approach; do not redo the same edit.")
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

// revertLastTurnFiles puts back the files this turn's own tools changed,
// and leaves the conversation alone so the model can see what it did and
// why it was undone.
//
// It runs mid-turn, before endTurn has recorded the checkpoint's manifest,
// and a checkpoint without one restores the whole tree to the turn's
// snapshot: a file the user saved in their editor while the turn ran went
// back with it. So the manifest is built here from what the turn's file
// tools recorded (the provenance Changes): only those paths are put back,
// and only while they still hold what the tool left there. A path that
// differs from the snapshot for any other reason — the user, a hook, a
// check command, a shell command — is left as it is and named.
func (a *Agent) revertLastTurnFiles(ctx context.Context) ([]string, error) {
	manifest := a.turnOwnManifest(a.turnN)
	a.cp.mu.Lock()
	n := len(a.cp.all)
	unmanifested := n > 0 && !a.cp.all[n-1].Manifested
	a.cp.mu.Unlock()
	if unmanifested && len(manifest) == 0 {
		// Whatever this turn changed, it changed through the shell, and
		// there is no telling that apart from the user's own edits. The
		// turn stands, and its checkpoint stays for /undo.
		return nil, errors.New("this turn's file tools changed nothing, so there is nothing of its own to put back")
	}
	// Same three phases as Undo: pop under cp.mu, restore with the lock
	// dropped, persist afterwards.
	cp, err := a.popCheckpointForTurn(a.turnN)
	if err != nil {
		return nil, err
	}
	var own map[string]bool // the turn's own paths, when built here
	if !cp.Manifested {
		own = map[string]bool{}
		cp.Touched, cp.Manifested = manifest, true
		for _, tf := range cp.Touched {
			own[tf.Path] = true
		}
		// A copy of a path the turn's tools touched but did not change
		// (a failed or no-op call) has nothing to put back.
		var files []Snapshot
		for _, f := range cp.Files {
			if own[f.Path] {
				files = append(files, f)
			}
		}
		cp.Files = files
	}
	done, err := a.restore(ctx, cp, true)
	// restore explains a path outside the manifest as one changed after
	// the turn ended; here the turn has not ended, so say what it is.
	for i, d := range done {
		rest, ok := strings.CutPrefix(d, "left ")
		if !ok || own == nil {
			continue
		}
		if path, _, ok := strings.Cut(rest, " alone: "); ok && !own[path] {
			done[i] = "left " + path + " alone: this turn's file tools did not change it (a shell command, a check, or an edit outside janetcode did)"
		}
	}
	if serr := a.SaveUndo(); serr != nil {
		a.debugf("save undo: %v", serr)
	}
	return done, err
}

// turnOwnManifest is the manifest of what turn's file tools left in the
// tree, from the provenance record: each path they changed, with the hash
// of the content the last of them wrote ("" when it deleted the path).
// A path the record has trimmed away is missing, so it is left alone —
// the safe direction.
func (a *Agent) turnOwnManifest(turn int) []TouchedFile {
	last := map[string]Change{}
	var order []string
	for _, c := range a.Changes() {
		if c.Turn != turn || c.Tool == "undo" {
			continue
		}
		if _, seen := last[c.Path]; !seen {
			order = append(order, c.Path)
		}
		last[c.Path] = c
	}
	out := make([]TouchedFile, 0, len(order))
	for _, p := range order {
		tf := TouchedFile{Path: p}
		if c := last[p]; !c.Gone {
			tf.Sum = fmt.Sprintf("%x", sha256.Sum256(c.After))
		}
		out = append(out, tf)
	}
	return out
}
