package agent

import (
	"context"
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

// wrapStopAfter is the model's tool calls after the wrap-up note at which
// the turn ends while the work is passing. The note asks for an answer at
// once, so ten more calls is the model having said no.
const wrapStopAfter = 10

// wrapStopState counts the tool calls made in the steps after the one that
// carried the wrap-up note. It lives beside wrap (turnmon.go resets both).
type wrapStopState struct{ calls int }

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
func (a *Agent) wrapStopCheck(n int, wasFired bool) string {
	if !a.WrapStop || a.Off["wrapstop"] || !a.wrap.fired || a.reviewSteps >= 0 || a.DryRunOn() || a.Log == nil {
		return ""
	}
	if wasFired {
		a.wrapStop.calls += n
	}
	if a.wrapStop.calls < wrapStopAfter {
		return ""
	}
	by := a.VerifiedBy()
	if by == "" {
		by = projectGreen(a.Log.Events(), a.turnN, a.displayRoot())
	}
	if by == "" {
		return ""
	}
	by, _, _ = strings.Cut(by, "\n")
	return textfmt.TruncateTo(by, 120)
}

// stopForWrap ends the turn the way a model's final answer does, minus the
// gates. The history ends on the step's tool results, so the closing text
// is appended as the assistant's reply, the verdict is logged and shown
// as finishTurn does, and the turn/end and EvTurnDone follow: RunBlocks
// returns nil, the run exits 0 and headless output is built from the same
// events as for any finished turn.
//
// finishTurn itself is bypassed on purpose. Its gates send a model that
// says it is done back for more calls (stop hook, verify, spec review,
// edge rules ...), and the wrap-up note is a reply to a model that cannot
// stop checking: a note that sent it back after ten ignored ones would
// undo the bound, and specreview and edgerules would read the change a
// second time at a cost the run has already overspent.
func (a *Agent) stopForWrap(ctx context.Context, check string, emit func(Event)) {
	reply := fmt.Sprintf("Stopped by the harness: the change passes `%s` and further checking was not changing it.", check)
	msg := llm.TextMessage(llm.RoleAssistant, reply)
	a.appendMessage(msg)
	a.logEvent(session.Event{Type: session.TypeAssistant, Turn: a.turnN, Step: a.stepN, Surface: true, Message: &msg})
	emit(Event{Kind: EvText, Text: reply})
	emit(Event{Kind: EvTextBlockDone})
	emit(Event{Kind: EvNotice, Text: fmt.Sprintf("turn stopped: %d calls after the wrap-up note and the change still passes", a.wrapStop.calls)})
	outcome := a.Outcome()
	a.logOutcome(outcome)
	a.refreshProofNotes(ctx, emit)
	if !a.Off["verdict"] && outcome.Verdict != VerdictNoChanges {
		emit(Event{Kind: EvVerdict, Text: outcome.Line(), IsError: !outcome.OK()})
	}
	a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "wrapstop"})
	emit(Event{Kind: EvTurnDone, Usage: a.TurnUsage(), StopReason: llm.StopEndTurn})
}
