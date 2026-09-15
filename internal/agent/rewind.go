// The conversation half of "/undo turn n": putting the context back to
// where it stood when that turn began, so the model stops believing it
// did work the work tree no longer holds.
//
// undo_turn.go restores the files. On its own that leaves the model
// asserting contents that are not there any more and offering to build
// on them — exactly the confusion "/undo turn" exists to end — so unless
// the user asks for --files-only the same command rewinds the
// conversation with the tree.
//
// The session log is the source of truth and stays append-only: nothing
// is cut out of the file. A replacement event is appended that shadows
// the surface events of the dropped turns and stands in their place (the
// same SurfaceOp compaction uses), and the live conversation is then
// re-folded from the log by exactly the path OpenSession takes on a
// resume. So what the model sees now and what a later --resume sees are
// the same thing, and they are that way for the same reason.
package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/undoabout"
)

// ConversationRewind is what a rewind drops from the context; see
// undoabout.Rewind, where it lives so both front-ends and the report
// formatter can word it without importing this package.
type ConversationRewind = undoabout.Rewind

// ErrTurnRunning refuses a turn restore while a turn is in flight. Both
// front-ends already keep /undo off the key handler while a turn runs
// (slash.idleOnly), but a rewind rewrites the very history the turn
// goroutine is appending to, so the agent refuses on its own account
// rather than trusting every caller to have checked.
var ErrTurnRunning = errors.New("a turn is running: wait for it to finish before restoring to an earlier turn")

// rewindNoteFiles caps how many restored paths the note names, so a
// restore touching a thousand files does not spend the context on a
// list the model cannot use anyway.
const rewindNoteFiles = 20

// previewRewind is what rewinding the conversation to a checkpoint would
// cost, without touching anything. It is nil when the turn's place in
// the live conversation cannot be found any more, and why then says
// which of the reasons it is, for the preview to pass on: leaving the
// user to guess whether the model will still believe in the reverted
// work is worse than a line of explanation.
func (a *Agent) previewRewind(cp Checkpoint) (rw *ConversationRewind, why string) {
	keep, ok, why := a.rewindCut(cp)
	if !ok {
		return nil, why
	}
	turns, archived, _ := a.rewindScan(cp.Turn)
	return &ConversationRewind{
		FromTurn: cp.Turn,
		Turns:    turns,
		Messages: len(a.Messages) - keep,
		Archived: len(archived),
	}, ""
}

// rewindCut is where the live conversation would be cut to go back to
// before the checkpoint's turn: how many of its messages are kept. The
// checkpoint records the message count at the moment its turn began, and
// that is an index into a history the harness may since have rewritten —
// a compaction replaces a whole head with one summary — so the index is
// checked against the log rather than trusted. ok is false when the
// point cannot be located, and why says so in words a preview can show.
func (a *Agent) rewindCut(cp Checkpoint) (keep int, ok bool, why string) {
	if cp.Turn <= 0 {
		return 0, false, "this checkpoint is older than the conversation bookkeeping, so where its turn began in the context was never recorded"
	}
	moved := fmt.Sprintf("the conversation has been rewritten since turn %d began (a compaction), so that point can no longer be located in it", cp.Turn)
	nothing := fmt.Sprintf("the conversation holds nothing from turn %d on", cp.Turn)
	if cp.Messages < 0 || cp.Messages > len(a.Messages) {
		return 0, false, moved
	}
	if a.Log == nil {
		// Nothing to check the index against; the conversation is only
		// in memory anyway, so the count is all there is.
		if cp.Messages == len(a.Messages) {
			return 0, false, nothing
		}
		return cp.Messages, true, ""
	}
	ev := a.Log.Events()
	seqs := session.CurrentSeqs(ev)
	if len(seqs) != len(a.Messages) {
		return 0, false, moved
	}
	if cp.Messages == len(seqs) {
		return 0, false, nothing
	}
	turnOf := make(map[int]int, len(ev))
	for _, e := range ev {
		turnOf[e.Seq] = e.Turn
	}
	// The message at the cut must be the turn's own, and the one before
	// it must not be: that is what says the index still points at the
	// boundary it was taken at.
	if turnOf[seqs[cp.Messages]] < cp.Turn {
		return 0, false, moved
	}
	if cp.Messages > 0 && turnOf[seqs[cp.Messages-1]] >= cp.Turn {
		return 0, false, moved
	}
	return cp.Messages, true, ""
}

// rewindScan reads the session log once for everything a rewind needs to
// know about turn n and the turns after it: how many turns they are, the
// archived tool outputs they produced (see TypeSpillSave), and the plan
// mode the session was in when turn n began — folded over what survives,
// since a dropped turn may have switched it.
func (a *Agent) rewindScan(n int) (turns int, archived []string, plan bool) {
	if a.Log == nil {
		// Nothing to fold; the mode stays as it is.
		if a.turnN >= n {
			turns = a.turnN - n + 1
		}
		return turns, nil, a.Settings().PlanMode
	}
	// Folded from false, exactly as session.FoldPlanMode does on a
	// resume: a session with no plan/mode event before turn n was not in
	// plan mode when it began.
	seen := map[int]bool{}
	for _, e := range a.Log.Events() {
		if e.Turn < n {
			if e.Type == session.TypePlanMode && e.Active != nil {
				plan = *e.Active
			}
			continue
		}
		seen[e.Turn] = true
		if e.Type == session.TypeSpillSave && e.Output != "" {
			archived = append(archived, e.Output)
		}
	}
	return len(seen), archived, plan
}

// rewindConversation puts the context back to how it stood when the
// checkpoint's turn began: the messages of that turn and everything
// after it stop reaching the model, one note stands where they were
// saying what happened and which files went back, and the state that
// counts messages is reset with them. restored is the restore's own
// report lines, already relative to the display root.
//
// Called with cp.mu not held, after the work tree is back — the files
// first and the conversation second, the order Undo uses, so a failed
// restore never leaves the model told about a revert that did not
// happen. Returns nil when there was nothing to rewind.
func (a *Agent) rewindConversation(cp Checkpoint, restored []string) *ConversationRewind {
	rw, _ := a.previewRewind(cp)
	if rw == nil {
		return nil
	}
	keep, _, _ := a.rewindCut(cp)
	_, archived, plan := a.rewindScan(cp.Turn)
	note := llm.HarnessMessage(llm.RoleUser, rewindNote(*rw, restored))
	if !a.rewindThroughLog(keep, note) {
		// No log, or a log whose surface no longer lines up with the
		// live messages one for one. The conversation is still cut at
		// the checkpoint's mark — the same truncation an /undo of a
		// single turn does — it just cannot be recorded for a resume.
		a.setMessages(append(a.Messages[:keep:keep], note))
	}
	a.undoRemoved = rw.Messages

	// The outputs of the dropped turns leave the archive with them: the
	// messages holding their ids are gone, so nothing can page through
	// them any more, and an id that outlived its turn would hand the
	// model the output of work that no longer exists.
	for _, id := range archived {
		a.Archive.Drop(id)
	}

	// Counters and the context accounting, reset the way a resume
	// rebuilds them. The turn counter deliberately is not wound back:
	// the log keeps the dropped turns (it is append-only), so reusing
	// their numbers would key two different turns the same — which is
	// exactly what RestoreFromLog refuses to do on a resume. The next
	// turn carries on after the highest number the log has ever seen.
	a.stepN = 0
	if plan != a.Settings().PlanMode {
		// A dropped turn switched plan mode; put it back and log it, so
		// a resume folds the value the session actually has.
		a.SetPlanMode(plan)
	}
	if s, ok := a.Log.(*session.Session); ok {
		// The pins live in the session metadata, which a rewind does not
		// rewrite; re-reading them is the line OpenSession runs, and it
		// keeps the live set and the file in step either way.
		a.SetPins(s.Meta.Pins)
	}
	// Compaction's accounting indexes the history by position:
	// lastInputMsgs says how many messages the last call's token count
	// covered, and EstimateTokens trusts it for everything below that
	// index. Those messages are gone, so the estimate would be a number
	// about a conversation that no longer exists. rewritten() drops it,
	// tells the integrity check the shrink was legitimate, and marks the
	// coming cache miss as expected rather than waste.
	a.contextManager.rewritten()
	a.EstimateTokens()
	return rw
}

// What a rewind deliberately leaves alone, and why:
//
//   - The cumulative usage and the session's stats. The tokens were
//     spent whether or not the work survived; a cost counter that went
//     down when work was thrown away would be a lie about the bill.
//   - Subagents. Each holds its own conversation and never writes the
//     parent's, so nothing of theirs is inconsistent afterwards. Their
//     reports were tool results in the dropped turns and go with them,
//     which is the point: the model no longer knows about work it can no
//     longer see the result of. A child is still continuable by id for
//     anything that kept one.
//   - Cron jobs (/loop). They carry no turn, and they are standing
//     instructions from the user rather than something a turn produced,
//     so scoping them to a rewound turn would be a guess. They stay
//     armed and die with the session as they always have.
//   - The /accept lock. It lives in the session metadata, not in the
//     conversation, and each locked test file is hash-stamped, so a
//     restore that puts one of them back is caught by the gate's own
//     check rather than silently trusted.
//   - The provenance record behind /changes and /blame, which gains the
//     restore as a change of its own. It is the history of what was done
//     to the files, and a rewind is one more thing that was done to
//     them.

// rewindThroughLog appends the replacement event that takes everything
// past keep off the model-visible surface and re-folds the conversation
// from the log, the way Open does on a resume. It reports whether it
// could: the live messages and the log's current surface must still line
// up one for one, which is the same assumption compaction makes when it
// shadows a head by index.
func (a *Agent) rewindThroughLog(keep int, note llm.Message) bool {
	if a.Log == nil {
		return false
	}
	seqs := session.CurrentSeqs(a.Log.Events())
	if len(seqs) != len(a.Messages) || keep > len(seqs) {
		return false
	}
	dropped := append([]int(nil), seqs[keep:]...)
	if len(dropped) == 0 {
		return false
	}
	// Shadowing by position rather than by turn number is what makes a
	// rewind past a compaction behave: a summary written during a
	// dropped turn stands where the messages it replaced stood, which is
	// before the cut, so it survives and the turns it speaks for are not
	// lost with the turns that summarised them.
	a.logEvent(session.Event{
		Type:      session.TypeUserMessage,
		Turn:      a.turnN,
		Surface:   true,
		SurfaceOp: session.SurfaceOpReplace,
		Replaces:  dropped,
		Message:   &note,
	})
	a.setMessages(session.FoldSurface(a.Log.Events()))
	return true
}

// rewindNote is what the model is told in place of the turns that were
// dropped. It is a harness note, not words in the user's mouth, and it
// says the three things the model cannot work out for itself: that the
// work is gone, that it is no longer the author of it, and that whatever
// earlier messages said about these files is no longer true.
func rewindNote(rw ConversationRewind, restored []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[harness] The user ran /undo turn %d. The work tree was put back to how it stood before that turn, and this conversation was rewound with it: %s after that point are no longer part of your context. You did not do that work; do not describe it as done.\n",
		rw.FromTurn, rw.Line())
	if len(restored) > 0 {
		sb.WriteString("The work tree changed back:\n")
		shown := restored
		if len(shown) > rewindNoteFiles {
			shown = shown[:rewindNoteFiles]
		}
		for _, r := range shown {
			sb.WriteString("  " + r + "\n")
		}
		if n := len(restored) - len(shown); n > 0 {
			fmt.Fprintf(&sb, "  ... and %d more\n", n)
		}
	}
	sb.WriteString("Read any file again before relying on what an earlier message said about it.")
	return sb.String()
}
