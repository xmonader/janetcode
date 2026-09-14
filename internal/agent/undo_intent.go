package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/slash"
)

// Selection is the answer to "which of this session's changes match a
// plain-language description": Undo is what would be reverted, Keep is
// everything else considered (so a preview can show both sides), Mixed is
// a change the model could not cleanly put on either side (see Mixed),
// Unavailable is a change excluded from consideration entirely because its
// own edit can no longer be located (see partitionUndoable), Why is one
// line explaining the split, Fallback marks a selection made by keyword
// overlap rather than a model, because none was configured or its reply
// could not be used, and Warnings is the deterministic coupling guard's
// findings (see detectCoupling) — independent of how the split was made.
type Selection struct {
	Undo        []Change
	Keep        []Change
	Mixed       []Mixed
	Unavailable []UnavailableChange
	Why         string
	Fallback    bool
	Warnings    []string
}

// Mixed is a recorded change the model said contains both edits that
// match the undo description and edits that do not — a rename and a
// logging tweak made in the same write_file or edit_file call, say. It is
// placed on neither side of the split: choosing one would be a guess
// about which half of the change the user meant, so ApplyPendingUndo
// refuses while any are present unless run with force.
type Mixed struct {
	Change Change
	Why    string
}

// UnavailableChange is a recorded change SelectChanges left out of the
// model's (or the keyword fallback's) candidates entirely: its own edit
// could no longer be located in the file as it is now, so neither undoing
// nor keeping it is a real choice — the file was edited, or reverted (e.g.
// by `git checkout`), outside the session since it was recorded. See
// partitionUndoable.
type UnavailableChange struct {
	Change Change
	Why    string
}

// UndoReport is what UndoSelection did.
type UndoReport struct {
	// Reverted is one line per change actually undone, newest first (the
	// order they were applied), in UndoChange's own wording.
	Reverted []string
	// Refused is set when a revert stopped partway: the error from the
	// first change that refused, plus which of the selection's changes
	// were never attempted because of it.
	Refused string
	// Checks is the verify gate's summary line, run once after any
	// change was reverted; empty when nothing was reverted, or nothing
	// ran to verify it (see CheckNote and Diagnostics for why).
	Checks string
	// ChecksOK is nil when Checks was not run, else whether it passed.
	ChecksOK *bool
	// CheckNote explains why Checks is empty even though something was
	// reverted: no verify commands are configured, or they are but are
	// not trusted. Empty whenever Checks ran (or nothing was reverted).
	CheckNote string
	// Diagnostics is the language-server fallback's report, consulted only
	// when CheckNote is set (no verify check ran): one line per file any
	// of the selection's candidates touched — reverted or kept, so a kept
	// change that only builds against something an undone one just
	// removed is not missed — naming the file and either what the
	// language server found or that the file could not be checked, or a
	// plain admission that nothing verified the result when no diagnose
	// hook is available at all. Empty whenever Checks ran.
	Diagnostics string
}

// ErrNoChangesToUndo is returned by SelectChanges when this session has no
// change left that a description could match (everything is already
// undone, or nothing was ever changed).
var ErrNoChangesToUndo = errors.New("agent: no changes to undo in this session")

// ErrNothingSelected is returned by UndoSelection when the selection's
// Undo side is empty: there is nothing to revert.
var ErrNothingSelected = errors.New("agent: nothing selected to undo")

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
// changes: with a provider configured, one side model call (not added to
// the conversation, the same discipline as AskOnce) is shown a compact
// numbered list of the candidates and asked which to undo and which to
// keep; without a provider, or when the model's reply cannot be used, a
// BM25 keyword ranking over each candidate's intent, path and diff takes
// over and Selection.Fallback is set so the caller can say so.
func (a *Agent) SelectChanges(ctx context.Context, description string) (Selection, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return Selection{}, fmt.Errorf("empty description")
	}
	all := a.undoCandidates()
	if len(all) == 0 {
		return Selection{}, ErrNoChangesToUndo
	}
	// A candidate whose own edit can no longer be located is not a choice
	// between undo and keep at all — offering it either way would promise
	// something this guard cannot actually do. It is excluded before the
	// model or the keyword fallback ever sees it (see partitionUndoable).
	candidates, unavailable := partitionUndoable(a.displayRoot(), all)
	if len(candidates) == 0 {
		// Every change is still there but none can be reverted any more:
		// say which and why, rather than claiming there is nothing.
		return Selection{Unavailable: unavailable, Why: "every change left in this session was edited or reverted since"}, nil
	}
	sel, got := Selection{}, false
	if a.Settings().Provider != nil {
		if s, err := a.selectByModel(ctx, description, candidates); err == nil {
			sel, got = s, true
		}
	}
	if !got {
		sel = selectByKeywords(description, candidates)
	}
	sel.Unavailable = unavailable
	// The coupling guard runs regardless of how the split was made: a
	// model can miss it as easily as the keyword fallback can.
	sel.Warnings = detectCoupling(a.displayRoot(), sel)
	return sel, nil
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
}

// ErrNoPendingUndo is returned by ApplyPendingUndo when no "/undo about"
// has been previewed yet this session (or it was already applied).
var ErrNoPendingUndo = errors.New("agent: no previewed undo to apply; run /undo about <description> first")

// ErrUndoSelectionStale is returned by ApplyPendingUndo when the
// session's undoable changes moved since the preview: something was
// undone, reverted, or added.
var ErrUndoSelectionStale = errors.New("agent: the session's changes moved since /undo about; run it again")

// ErrUndoNeedsForce is returned by ApplyPendingUndo when the previewed
// selection has a change the model flagged as Mixed, or the deterministic
// coupling guard raised a Warning, and force was not passed: acting on
// either without a deliberate override risks leaving the tree half
// consistent, so the caller must re-run as "/undo apply --force".
var ErrUndoNeedsForce = errors.New("agent: this selection has mixed changes or coupling warnings; rerun as /undo apply --force to proceed anyway")

// PreviewUndoAbout selects changes matching description and remembers the
// selection as the session's pending undo, for a later ApplyPendingUndo.
func (a *Agent) PreviewUndoAbout(ctx context.Context, description string) (Selection, error) {
	sel, err := a.SelectChanges(ctx, description)
	if err != nil {
		return Selection{}, err
	}
	a.pendingUndo = &pendingUndo{description: description, fingerprint: fingerprintChanges(a.undoCandidates()), sel: sel}
	return sel, nil
}

// ApplyPendingUndo reverts the selection from the last PreviewUndoAbout.
// Unless force is true, it refuses when the selection has any Mixed
// change or coupling Warning, leaving the preview in place so the caller
// can re-run with force rather than having to preview again.
func (a *Agent) ApplyPendingUndo(ctx context.Context, force bool) (UndoReport, error) {
	p := a.pendingUndo
	if p == nil {
		return UndoReport{}, ErrNoPendingUndo
	}
	if fingerprintChanges(a.undoCandidates()) != p.fingerprint {
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
	return cmdresult.Result{Sheet: FormatSelection(root, sel), SheetTitle: "undo about"}, nil
}

// UndoApply runs /undo apply [--force] for every front-end: it reverts
// the selection from the last /undo about, under UndoApplyTimeout. The
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
	return rep, cmdresult.Result{Sheet: FormatUndoReport(rep), SheetTitle: "undo"}, nil
}

// fingerprintChanges identifies the set of undoable changes at a moment
// in time, cheaply enough to call on every preview and apply: it changes
// whenever a candidate is undone, added, or removed.
func fingerprintChanges(candidates []Change) string {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = fmt.Sprintf("%d", c.Seq)
	}
	return strings.Join(ids, ",")
}

// displayRoot is the directory paths shown by /undo about are relative
// to: Root, or the process working directory when Root is unset; "" (show
// absolute paths) when neither is known.
func (a *Agent) displayRoot() string {
	if a.Root != "" {
		return a.Root
	}
	d, _ := os.Getwd()
	return d
}

// UndoArgs is "/undo"'s parsed arguments, shared by every front-end (see
// ParseUndoArgs) so "/undo ..." means exactly the same thing in the
// terminal and the web UI. The zero value is a bare "/undo": revert the
// last turn's whole checkpoint.
type UndoArgs struct {
	// Preview lists what a bare /undo would revert, without touching
	// anything.
	Preview bool
	// N is a specific /changes number to revert on its own; zero when
	// not given.
	N int
	// About previews the selection matching Description, without
	// touching anything (see PreviewUndoAbout). About is set whenever the
	// first word is "about", even with an empty Description — the caller
	// reports that as its own usage error, worded for its front-end.
	About       bool
	Description string
	// Apply reverts the selection from the last About (see
	// ApplyPendingUndo). Force is --force: proceed even though the
	// selection has a Mixed change or a coupling Warning.
	Apply bool
	Force bool
}

// errUndoArgsUsage documents /undo's argument shape (kept in step with
// slash.Command "/undo" and webArgs["/undo"]).
var errUndoArgsUsage = errors.New("usage: /undo [preview | n | about <description> | apply [--force]]")

// ParseUndoArgs parses the text after "/undo" (leading/trailing space
// trimmed by the caller or here either way): the one shared parser both
// the terminal and the web UI dispatch through, so a spelling accepted by
// one is accepted, and means the same thing, in the other.
func ParseUndoArgs(rest string) (UndoArgs, error) {
	verb, after := slash.CutWord(rest)
	switch verb {
	case "":
		return UndoArgs{}, nil
	case "about":
		return UndoArgs{About: true, Description: strings.TrimSpace(after)}, nil
	}
	t, err := slash.Tokenize(after, slash.ArgSpec{Bool: []string{"--force"}})
	if err != nil {
		return UndoArgs{}, errUndoArgsUsage
	}
	switch verb {
	case "preview":
		if len(t.Positional) > 0 || len(t.Flags) > 0 {
			return UndoArgs{}, errUndoArgsUsage
		}
		return UndoArgs{Preview: true}, nil
	case "apply":
		if len(t.Positional) > 0 {
			return UndoArgs{}, errUndoArgsUsage
		}
		return UndoArgs{Apply: true, Force: t.Has("--force")}, nil
	default:
		if n, err := strconv.Atoi(verb); err == nil && len(t.Positional) == 0 && len(t.Flags) == 0 {
			return UndoArgs{N: n}, nil
		}
		return UndoArgs{}, errUndoArgsUsage
	}
}
