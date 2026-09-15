// Package undoabout is the model-driven half of "/undo about": deciding
// which of a session's recorded changes a plain-language description
// means, warning when undoing them would leave the tree inconsistent, and
// rendering the answer. It reverts nothing itself — internal/agent owns
// the checkpoints and applies the selection — and it never imports that
// package.
package undoabout

import (
	"errors"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/change"
	argspkg "github.com/xmonader/janetcode/internal/slash/args"
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
	Undo        []change.Change
	Keep        []change.Change
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
	Change change.Change
	Why    string
}

// UnavailableChange is a recorded change SelectChanges left out of the
// model's (or the keyword fallback's) candidates entirely: its own edit
// could no longer be located in the file as it is now, so neither undoing
// nor keeping it is a real choice — the file was edited, or reverted (e.g.
// by `git checkout`), outside the session since it was recorded. See
// partitionUndoable.
type UnavailableChange struct {
	Change change.Change
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
var ErrNoChangesToUndo = errors.New("no changes to undo in this session")

// ErrNothingSelected is returned by UndoSelection when the selection's
// Undo side is empty: there is nothing to revert.
var ErrNothingSelected = errors.New("nothing selected to undo")

// Args is "/undo"'s parsed arguments, shared by every front-end (see
// ParseArgs) so "/undo ..." means exactly the same thing in the
// terminal and the web UI. The zero value is a bare "/undo": revert the
// last turn's whole checkpoint.
type Args struct {
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

// errArgsUsage documents /undo's argument shape (kept in step with
// slash.Command "/undo" and webArgs["/undo"]).
var errArgsUsage = errors.New("usage: /undo [preview | n | about <description> | apply [--force]]")

// ParseArgs parses the text after "/undo" (leading/trailing space
// trimmed by the caller or here either way): the one shared parser both
// the terminal and the web UI dispatch through, so a spelling accepted by
// one is accepted, and means the same thing, in the other.
func ParseArgs(rest string) (Args, error) {
	verb, after := argspkg.CutWord(rest)
	switch verb {
	case "":
		return Args{}, nil
	case "about":
		return Args{About: true, Description: strings.TrimSpace(after)}, nil
	}
	t, err := argspkg.Tokenize(after, argspkg.Spec{Bool: []string{"--force"}})
	if err != nil {
		return Args{}, errArgsUsage
	}
	switch verb {
	case "preview":
		if len(t.Positional) > 0 || len(t.Flags) > 0 {
			return Args{}, errArgsUsage
		}
		return Args{Preview: true}, nil
	case "apply":
		if len(t.Positional) > 0 {
			return Args{}, errArgsUsage
		}
		return Args{Apply: true, Force: t.Has("--force")}, nil
	default:
		if n, err := strconv.Atoi(verb); err == nil && len(t.Positional) == 0 && len(t.Flags) == 0 {
			return Args{N: n}, nil
		}
		return Args{}, errArgsUsage
	}
}

// TouchedPaths lists, without duplicates, every file any change
// sel considered touches: undone, kept, or left mixed.
func TouchedPaths(sel Selection) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, c := range sel.Undo {
		add(c.Path)
	}
	for _, c := range sel.Keep {
		add(c.Path)
	}
	for _, m := range sel.Mixed {
		add(m.Change.Path)
	}
	return out
}
