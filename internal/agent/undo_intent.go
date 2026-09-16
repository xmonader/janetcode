package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/undoabout"
)

// undoCandidates are this session's changes eligible for description
// matching: still in effect, and not themselves the record of an earlier
// undo (reverting a revert is not what "undo about X" means).
func (a *Agent) undoCandidates() []Change {
	var out []Change
	for _, c := range a.Changes() {
		if c.Undone || c.Tool == "undo" {
			continue
		}
		out = append(out, c)
	}
	return out
}

// SelectChanges matches description against this session's undoable
// changes; see undoabout.Select, which does the matching, the coupling
// guard and the unavailable-candidate partition.
func (a *Agent) SelectChanges(ctx context.Context, description string) (Selection, error) {
	st := a.Settings()
	prov, model := st.Provider, st.Model
	if a.Off["undoabout"] {
		// Keyword overlap is what Select did before the model-driven
		// split existed; passing no provider is that path.
		prov, model = nil, ""
	}
	return undoabout.Select(ctx, prov, model, a.displayRoot(), description, a.undoCandidates())
}

// pendingUndo is the last selection shown by "/undo about", kept so
// "/undo apply" can act on exactly what the user saw rather than asking
// the model again (which could disagree with itself, or fail where the
// first call did not). fingerprint is the candidate seqs at preview time;
// apply refuses once they no longer match, rather than silently reverting
// a different set of changes than the one shown.
type pendingUndo struct {
	description string
	fingerprint string
	sel         Selection
	// turn and cp are set instead of sel when the preview was "/undo
	// turn n": the whole work tree goes back to the checkpoint that turn
	// started from, rather than a chosen set of changes coming out of it
	// (see undo_turn.go). One pending selection, two things it can hold,
	// so both are confirmed and applied the same way.
	turn *TurnRestore
	cp   Checkpoint
}

// ErrNoPendingUndo is returned by ApplyPendingUndo when no "/undo about"
// has been previewed yet this session (or it was already applied).
var ErrNoPendingUndo = errors.New("no previewed undo to apply; run /undo about <description> or /undo turn <n> first")

// ErrUndoSelectionStale is returned by ApplyPendingUndo when the
// session's undoable changes moved since the preview: something was
// undone, reverted, or added.
var ErrUndoSelectionStale = errors.New("the session's changes moved since the preview; run /undo about or /undo turn again")

// ErrUndoNeedsForce is returned by ApplyPendingUndo when the previewed
// selection has a change the model flagged as Mixed, or the deterministic
// coupling guard raised a Warning, and force was not passed: acting on
// either without a deliberate override risks leaving the tree half
// consistent, so the caller must re-run as "/undo apply --force".
var ErrUndoNeedsForce = errors.New("this selection has mixed changes or coupling warnings; rerun as /undo apply --force to proceed anyway")

// PreviewUndoAbout selects changes matching description and remembers the
// selection as the session's pending undo, for a later ApplyPendingUndo.
func (a *Agent) PreviewUndoAbout(ctx context.Context, description string) (Selection, error) {
	sel, err := a.SelectChanges(ctx, description)
	if err != nil {
		return Selection{}, err
	}
	a.pendingUndo = &pendingUndo{description: description, fingerprint: undoabout.Fingerprint(a.undoCandidates()), sel: sel}
	return sel, nil
}

// ApplyPendingUndo carries out the last preview: the selection from
// PreviewUndoAbout, or the whole-tree restore from PreviewUndoTurn.
// Unless force is true, it refuses when a selection has any Mixed change
// or coupling Warning, leaving the preview in place so the caller can
// re-run with force rather than having to preview again; a turn restore
// has neither, so force means nothing to it.
func (a *Agent) ApplyPendingUndo(ctx context.Context, force bool) (UndoReport, error) {
	p := a.pendingUndo
	if p == nil {
		return UndoReport{}, ErrNoPendingUndo
	}
	if p.turn != nil {
		if a.running.Load() {
			return UndoReport{}, ErrTurnRunning
		}
		a.cp.mu.Lock()
		stale := a.turnFingerprint() != p.fingerprint
		a.cp.mu.Unlock()
		if stale {
			// The preview is left in place: it is the turns that moved,
			// not the user's mind, so /undo turn can be run again.
			return UndoReport{}, ErrUndoSelectionStale
		}
		a.pendingUndo = nil
		return a.applyTurnRestore(ctx, *p.turn, p.cp, p.fingerprint)
	}
	if undoabout.Fingerprint(a.undoCandidates()) != p.fingerprint {
		return UndoReport{}, ErrUndoSelectionStale
	}
	if !force && (len(p.sel.Mixed) > 0 || len(p.sel.Warnings) > 0) {
		return UndoReport{}, ErrUndoNeedsForce
	}
	a.pendingUndo = nil
	return a.UndoSelection(ctx, p.sel)
}

// UndoAboutTimeout and UndoApplyTimeout bound /undo about (a model call
// matching the description) and /undo apply (which can run the project's
// checks) in every front-end.
const (
	UndoAboutTimeout = 2 * time.Minute
	UndoApplyTimeout = 5 * time.Minute
)

// ErrUndoAboutUsage is /undo about with no description.
var ErrUndoAboutUsage = errors.New("usage: /undo about <description>")

// UndoAbout runs /undo about <description> for every front-end: it
// previews which of this session's changes match, under
// UndoAboutTimeout, shown relative to root. Nothing is touched.
func (a *Agent) UndoAbout(ctx context.Context, root, description string) (cmdresult.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, UndoAboutTimeout)
	defer cancel()
	sel, err := a.PreviewUndoAbout(ctx, description)
	if err != nil {
		return cmdresult.Result{}, fmt.Errorf("undo about: %w", err)
	}
	return cmdresult.Result{Sheet: undoabout.FormatSelection(root, sel), SheetTitle: "undo about"}, nil
}

// UndoApply runs /undo apply [--force] for every front-end: it carries
// out the last /undo about or /undo turn preview, under UndoApplyTimeout. The
// caller must pass the returned report to TellModelUndone — at the point
// where it owns the agent's history — so the model does not go on
// believing the changes are still there.
func (a *Agent) UndoApply(ctx context.Context, force bool) (UndoReport, cmdresult.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, UndoApplyTimeout)
	defer cancel()
	rep, err := a.ApplyPendingUndo(ctx, force)
	if err != nil {
		return UndoReport{}, cmdresult.Result{}, fmt.Errorf("undo apply: %w", err)
	}

	return rep, cmdresult.Result{Sheet: undoabout.FormatUndoReport(rep), SheetTitle: "undo"}, nil
}

// displayRoot is the directory paths shown by /undo about are relative
// to: Root, or the process working directory when Root is unset; "" (show
// absolute paths) when neither is known.
func (a *Agent) displayRoot() string {
	if r := a.RootDir(); r != "" {
		return r
	}
	d, _ := os.Getwd()
	return d
}
