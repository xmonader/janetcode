package agent

import (
	"github.com/xmonader/janetcode/internal/undoabout"
)

// "/undo about" itself — matching a description against the session's
// changes, the coupling guard and the rendering — lives in
// internal/undoabout. What stays here is the session state it needs
// (the changes, the pending selection) and applying the answer, which
// means the checkpoints. These aliases keep the names the front-ends
// already use.

// Selection is the answer to "which of this session's changes match a
// plain-language description"; see undoabout.Selection.
type Selection = undoabout.Selection

// Mixed is a change the model could put on neither side of the split;
// see undoabout.Mixed.
type Mixed = undoabout.Mixed

// UnavailableChange is a change excluded from the split entirely; see
// undoabout.UnavailableChange.
type UnavailableChange = undoabout.UnavailableChange

// UndoReport is what UndoSelection did; see undoabout.UndoReport.
type UndoReport = undoabout.UndoReport

// UndoArgs is "/undo"'s parsed arguments; see undoabout.Args.
type UndoArgs = undoabout.Args

// ParseUndoArgs parses the text after "/undo"; see undoabout.ParseArgs.
func ParseUndoArgs(rest string) (UndoArgs, error) { return undoabout.ParseArgs(rest) }

// FormatSelection renders a selection for a front-end sheet.
func FormatSelection(cwd string, sel Selection) string { return undoabout.FormatSelection(cwd, sel) }

// FormatUndoReport renders an undo report for a front-end sheet.
func FormatUndoReport(rep UndoReport) string { return undoabout.FormatUndoReport(rep) }

var (
	// ErrNoChangesToUndo is returned when this session has no change left
	// that a description could match.
	ErrNoChangesToUndo = undoabout.ErrNoChangesToUndo
	// ErrNothingSelected is returned by UndoSelection when the selection's
	// Undo side is empty.
	ErrNothingSelected = undoabout.ErrNothingSelected
)
