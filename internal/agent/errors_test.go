package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/undoabout"
)

// Errors from this package reach the user as they are ("✗ undo apply:
// agent: this selection…"), so none starts with the package's name, and
// a front-end's own prefix is not repeated ("accept: accept: …").
func TestUserFacingErrorsCarryNoPackagePrefix(t *testing.T) {
	for _, err := range []error{ErrMaxIterations, ErrNothingToCompact, ErrStuck, undoabout.ErrNoChangesToUndo, undoabout.ErrNothingSelected, ErrNoPendingUndo, ErrUndoSelectionStale, ErrUndoNeedsForce} {
		if strings.HasPrefix(err.Error(), "agent:") {
			t.Errorf("%q starts with the package name", err)
		}
	}
	a := &Agent{}
	for name, args := range map[string]AcceptArgs{"lock": {Lock: true}, "unlock": {Unlock: true}, "command": {SetCommand: true, Command: "go test"}} {
		if _, err := a.AcceptCommand(args, t.TempDir()); err == nil || strings.Count(err.Error(), "accept:") != 1 {
			t.Errorf("%s: err = %v, want one accept: prefix", name, err)
		}
	}
}
