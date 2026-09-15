package agent

import (
	"context"
	"fmt"
	"github.com/xmonader/janetcode/internal/undoabout"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/verify"
)

// UndoSelection reverts sel.Undo, newest change first so a later edit to
// the same file is undone before an earlier one — the same order
// UndoChange itself expects hunks to disappear in. It stops at the first
// refusal, keeping what was already reverted; whenever anything was
// reverted, reportChecks fills in how (or whether) the result was
// verified, so a revert never finishes silent about it.
func (a *Agent) UndoSelection(ctx context.Context, sel Selection) (UndoReport, error) {
	if len(sel.Undo) == 0 {
		return UndoReport{}, ErrNothingSelected
	}
	undo := append([]Change(nil), sel.Undo...)
	sort.Slice(undo, func(i, j int) bool { return undo[i].Seq > undo[j].Seq })
	var rep UndoReport
	for i, c := range undo {
		done, err := a.UndoChange(c.Seq)
		if err != nil {
			var remaining []string
			for _, r := range undo[i:] {
				remaining = append(remaining, fmt.Sprintf("#%d", r.Seq))
			}
			rep.Refused = fmt.Sprintf("%v (not reverted: %s)", err, strings.Join(remaining, ", "))
			break
		}
		rep.Reverted = append(rep.Reverted, done)
	}
	if len(rep.Reverted) > 0 {
		a.reportChecks(ctx, &rep, sel)
	}
	return rep, nil
}

// reportChecks fills in rep's verification fields after at least one
// change from a selection was reverted. It runs the project's verify gate
// when one is trusted and configured; otherwise it explains why not
// (VerifyUntrusted, or nothing configured at all) and, since no checks
// ran, falls back to language-server diagnostics through a.Diagnose when
// one is wired up — the same hook a file tool's own edit uses (see
// executeTool in exec.go) — or admits plainly that nothing verified the result when
// there is no such hook either.
//
// The fallback covers every file any of sel's candidates touches, reverted
// or kept, not only the reverted ones: a kept change can just as easily
// stop building once an undone neighbor's definition disappears under it
// (see detectCoupling), and checking only the reverted side would report
// "no problems" without having looked at the file that actually broke.
// Each file gets its own line naming it, so the report never reads as a
// bare "no problems" when some touched file was never examined at all.
func (a *Agent) reportChecks(ctx context.Context, rep *UndoReport, sel Selection) {
	gate, untrusted := a.VerifyGate()
	if gate.Enabled() {
		res := gate.Run(ctx)
		ok := res.OK()
		rep.ChecksOK = &ok
		rep.Checks = res.Summary()
		return
	}
	if untrusted {
		rep.CheckNote = fmt.Sprintf("the project's %s is not trusted, so checks did not run (/verify trust)", verify.ProjectFile)
	} else {
		rep.CheckNote = "no checks configured (/verify set <cmd>)"
	}
	paths := undoabout.TouchedPaths(sel)
	if a.Diagnose == nil || len(paths) == 0 {
		rep.Diagnostics = "nothing verified the result"
		return
	}
	cwd := a.displayRoot()
	var notes []string
	for _, p := range paths {
		note := a.Diagnose(ctx, p)
		if note == "" {
			note = "not checked (no diagnostics available for this file)"
		}
		notes = append(notes, fmt.Sprintf("%s: %s", textfmt.RelTo(cwd, p), note))
	}
	rep.Diagnostics = strings.Join(notes, "\n")
}

// TellModelUndone adds a note to the conversation naming what a /undo
// apply reverted, so the model does not go on believing those changes are
// still there. A rewinding "/undo turn" has already said it in the
// conversation it rewound, so this adds nothing there. It is a harness note of its own (a synthetic block ending
// its paragraph), not words put in the user's mouth, so the prompt typed
// next reads as what it is. Call it between turns.
func (a *Agent) TellModelUndone(rep UndoReport) {
	if len(rep.Reverted) == 0 {
		return
	}
	if rep.Rewound != nil {
		// A turn restore that rewound the conversation has already put
		// its own note where the dropped turns were, naming the same
		// files (see rewind.go). A second note here would speak about
		// the turns from outside them and say it twice.
		return
	}
	a.AddHarnessContext("[harness] The user ran /undo apply, which reverted some of your earlier changes:\n" + strings.Join(rep.Reverted, "\n") + "\nLater changes to the same files stayed.\n")
}
