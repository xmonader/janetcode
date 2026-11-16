package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// A headless turn that goes on checking a passing change after the wrap-up
// note is stopped by the harness. On effbench (Kimi, syn-kimi-pi2
// a-catalog-paging-s2) the public tests passed at call 11, the note fired
// at call 30 and was ignored, and the model made 84 more calls (78 bash)
// debugging its own ad-hoc cursor check until the 20-minute cap: 95 calls,
// 3.3M input tokens, a FAIL. A note can be ignored; this is the bound.
// JANETCODE_OFF=wrapstop turns it off.
//
// The count restarts at an edit of a product file, as the wrap-up count
// does: the stop is for checking that changes nothing, and an edit is a
// change. Of the 36 Kimi runs the stop ended (syn-kimi-*, 10 passed), 20
// had edited a product file inside those ten calls. syn-kimi-plan noplan
// a-catalog-paging-s1 fixed its descending cursor, ran its own check
// (pages repeated items, the bug the hidden suite fails on) with pytest
// behind it, and was stopped on that call because pytest passed. Two
// restarts a turn, so a model that edits for ever is still ended.
// JANETCODE_OFF=wrapstopedit turns the restart off.

// wrapStopAfter is the model's tool calls after the wrap-up note at which
// the turn ends while the work is passing. The note asks for an answer at
// once, so ten more calls is the model having said no.
const wrapStopAfter = 10

// wrapStopState counts the tool calls made in the steps after the one that
// carried the wrap-up note. It lives beside wrap (turnmon.go resets both).
type wrapStopState struct{ calls, restarts int }

// wrapStopRestarts is how many times a turn's product edits restart the
// count: with the first ten, at most thirty calls after the note.
const wrapStopRestarts = 2

// projectGreen is the latest project test command of the turn (not the
// model's own check) when it passed and no edit succeeded after it, and
// "" otherwise. VerifiedBy cannot say this: the model's own check, run
// last and left red, clears it, and that is the very loop this ends (the
// tests were green and the own check was not, for 84 calls).
func projectGreen(events []session.Event, turn int, root string) string {
	var cmd string
	green := false
	created := map[string]bool{}
	for _, e := range events {
		if e.Turn != turn || e.Type != session.TypeToolResult || e.CallID == "" || refused(e) {
			continue
		}
		if editTool(e.Name) && !e.IsError {
			if e.Name == "write_file" && strings.HasPrefix(e.Output, "Created ") {
				if p := argPathOf(e.Arguments); p != "" {
					created[p] = true
				}
			}
			green = false
			continue
		}
		if e.Name != "bash" || neverRan(e) || unfinished(e) {
			continue
		}
		c := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
		own := scratchScript(c) || runsScript(c, func(w string) bool {
			if created[strings.TrimPrefix(w, "./")] || created[w] {
				return true
			}
			return filepath.IsAbs(w) && root != "" && !strings.HasPrefix(w, root+string(filepath.Separator))
		})
		if !own && shell.IsCheck(c) {
			cmd, green = c, !runFailed(e)
		}
	}
	if !green {
		return ""
	}
	return cmd
}

// wrapStopCheck is called after a tool step of n calls ran. wasFired is
// whether the wrap-up note had been given before this step: the step that
// carries the note is not counted. It returns the check the change passes
// when the turn should end now, "" otherwise. Past the tenth call it asks
// again after every step, so a red state at ten stops the first time it is
// green again; nothing else bounds a red loop here, the other limits do.
func (a *Agent) wrapStopCheck(uses, results []llm.Block, wasFired bool) string {
	if !a.WrapStop || a.Off["wrapstop"] || !a.wrap.fired || a.reviewSteps >= 0 || a.DryRunOn() || a.Log == nil {
		return ""
	}
	if wasFired {
		a.wrapStop.calls += len(uses)
		if !a.Off["wrapstopedit"] && a.wrapStop.restarts < wrapStopRestarts && editedProduct(uses, results) {
			a.wrapStop.calls = 0
			a.wrapStop.restarts++
		}
	}
	if a.wrapStop.calls < wrapStopAfter {
		return ""
	}
	by := a.VerifiedBy()
	if by == "" {
		by = projectGreen(a.logEvents(), a.turnN, a.displayRoot())
	}
	if by == "" {
		return ""
	}
	by, _, _ = strings.Cut(by, "\n")
	return textfmt.TruncateTo(by, 120)
}

// ErrVerification distinguishes an incomplete forced stop from success.
var ErrVerification = errors.New("turn stopped with failed or incomplete verification")

// stopForWrap ends model activity while preserving deterministic
// completion checks. Reviews and repair rounds cannot extend this stop.
func (a *Agent) stopForWrap(ctx context.Context, check string, emit func(Event)) error {
	// Stop buying model rounds, but retain the deterministic guarantees
	// of normal completion. Evaluate every gate even if an earlier one
	// fails; the final report must include all outstanding failures.
	var g turnGates
	g.verify.final = true
	failed := false
	if !a.Off["verify"] {
		failed = a.verifyGate(ctx, &g.verify, emit) || failed
	}
	failed = a.probeGate(ctx, &g.probes, emit) || failed
	failed = a.acceptVerifyGate(ctx, &g.accept, emit) || failed
	failed = a.testLedgerGate(ctx, &g.ledger, emit) || failed
	failed = a.diffFuzzGate(ctx, &g.difffuzz, emit) || failed
	if err := ctx.Err(); err != nil {
		return err
	}
	outcome := a.Outcome()
	failed = failed || !outcome.OK()
	if failed && outcome.OK() {
		// Integrity checks and probe findings need not look like shell
		// failures to the outcome fold. They must still invalidate a
		// previously green verdict and a completed goal.
		outcome.Verdict = VerdictCheckFailed
		outcome.Why = "a mandatory completion check failed"
	}
	reply := fmt.Sprintf("Stopped by the harness: the change passes `%s` and further checking was not changing it.", check)
	if failed {
		reply = "Stopped by the harness: verification failed or remains incomplete. " + outcome.Line()
	}
	msg := llm.TextMessage(llm.RoleAssistant, reply)
	a.appendMessage(msg)
	a.logEvent(session.Event{Type: session.TypeAssistant, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &msg})
	emit(Event{Kind: EvText, Text: reply})
	emit(Event{Kind: EvTextBlockDone})
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("turn stopped: %d calls after the wrap-up note", a.wrapStop.calls), IsError: failed})
	a.logOutcome(outcome)
	a.ReopenCompletedGoal(outcome, emit)
	a.refreshProofNotes(ctx, emit)
	if !a.Off["verdict"] && outcome.Verdict != VerdictNoChanges {
		emit(verdictEvent(outcome))
	}
	a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "wrapstop"})
	emit(Event{Kind: EvTurnDone, Usage: a.TurnUsage(), StopReason: llm.StopEndTurn})
	if failed {
		return ErrVerification
	}
	return nil
}
