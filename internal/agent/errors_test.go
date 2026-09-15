package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/critic"
	"github.com/xmonader/janetcode/internal/session"

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

// /why is the one thing that reads a refusal back off the ledger, and it
// recognises one by its wording (see critic's refusalRe). The constants
// live here, so this is the side that has to notice when one is reworded
// into something /why can no longer see.
func TestHarnessRefusalsAreVisibleToWhy(t *testing.T) {
	for _, out := range []string{DeniedMessage, "Permission denied: no approver configured for mutating tools."} {
		ivs := critic.Interventions([]session.Event{
			{Seq: 1, Type: session.TypeToolResult, Turn: 1, Name: "read_file", IsError: true, Output: out},
		})
		if len(ivs) != 1 || ivs[0].Kind != critic.IvRefused {
			t.Errorf("%q is not read as a refusal by /why: %v", out, ivs)
		}
	}
}
