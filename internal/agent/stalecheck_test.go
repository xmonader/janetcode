package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// staleEvents is the premature stop in its simplest form: edit, test,
// edit again, say it is done. Every total the verdict fold keeps is
// green and the last edit was never run against anything.
func staleEvents(editLast bool) []session.Event {
	msg := func(text string) *llm.Message { m := llm.TextMessage(llm.RoleAssistant, text); return &m }
	evs := []session.Event{
		{Seq: 1, Type: session.TypeTurnStart, Turn: 1},
		{Seq: 2, Type: session.TypeToolResult, Turn: 1, CallID: "a", Name: "edit_file",
			Arguments: `{"path":"parse.go"}`, Output: "Edited parse.go"},
		{Seq: 3, Type: session.TypeToolResult, Turn: 1, CallID: "b", Name: "bash",
			Arguments: `{"command":"go test ./..."}`, Output: "ok  parse\nPASS\n3 passed"},
	}
	if editLast {
		evs = append(evs, session.Event{Seq: 4, Type: session.TypeToolResult, Turn: 1, CallID: "c",
			Name: "edit_file", Arguments: `{"path":"parse.go"}`, Output: "Edited parse.go"})
	}
	return append(evs, session.Event{Seq: 5, Type: session.TypeAssistant, Turn: 1,
		Message: msg("Done — the tests pass.")})
}

func TestStaleCheckSeesAnEditAfterTheLastCheck(t *testing.T) {
	stale := FoldOutcome(staleEvents(true), 1)
	if !stale.EditedAfterCheck {
		t.Fatalf("an edit after the last check should be seen: %+v", stale)
	}
	if !stale.StaleCheck() {
		t.Fatalf("a claim over a stale green is the premature stop: %+v", stale)
	}
	if note := stale.StaleCheckNote(); !strings.Contains(note, "edited again afterwards") ||
		!strings.Contains(note, `"tests pass"`) {
		t.Fatalf("note = %q", note)
	}

	// The same turn with the last edit removed: the check is the last
	// thing that happened, so there is nothing to nudge about.
	fresh := FoldOutcome(staleEvents(false), 1)
	if fresh.EditedAfterCheck || fresh.StaleCheck() {
		t.Fatalf("a turn checked after its last edit must not be nudged: %+v", fresh)
	}
}

// The two ways the rule must stay quiet, both of which are already
// somebody else's job.
func TestStaleCheckDefersToTheClaimAndTheOverclaimGate(t *testing.T) {
	// No claim: the turn left work unverified and said nothing about it,
	// which is the verdict line's business, not an interruption's.
	quiet := staleEvents(true)
	quiet[len(quiet)-1].Message = nil
	if FoldOutcome(quiet, 1).StaleCheck() {
		t.Fatal("a turn that claimed nothing must not be nudged")
	}
	// A turn that never ran anything at all is VerdictUnverified and is
	// already sent back by the overclaim gate; nudging it twice for the
	// same thing is the noise this rule exists to avoid.
	noCheck := []session.Event{
		{Seq: 1, Type: session.TypeTurnStart, Turn: 1},
		{Seq: 2, Type: session.TypeToolResult, Turn: 1, CallID: "a", Name: "edit_file",
			Arguments: `{"path":"parse.go"}`, Output: "Edited parse.go"},
	}
	m := llm.TextMessage(llm.RoleAssistant, "Done — the tests pass.")
	noCheck = append(noCheck, session.Event{Seq: 3, Type: session.TypeAssistant, Turn: 1, Message: &m})
	o := FoldOutcome(noCheck, 1)
	if !o.Overclaimed() {
		t.Fatalf("a claim with nothing run is an overclaim: %+v", o)
	}
	if o.StaleCheck() {
		t.Fatalf("the overclaim gate already has this turn: %+v", o)
	}
}

// The gate itself: with the flag off the turn ends exactly as it did
// before the rule existed, and with it on it is sent back once and only
// once, sharing the one-correction-a-turn budget.
func TestStaleCheckNudgeIsOptInAndSendsBackOnce(t *testing.T) {
	agentAt := func(on bool) *Agent {
		a := &Agent{Log: &session.Memory{}, Off: map[string]bool{"verify": true}, StaleCheckNudge: on}
		a.turnN = 1
		for _, e := range staleEvents(true) {
			a.Log.Append(e)
		}
		return a
	}
	resp := &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "Done — the tests pass."),
		StopReason: llm.StopEndTurn}

	off := agentAt(false)
	var g turnGates
	if done := off.finishTurn(context.Background(), resp, &g, func(Event) {}); !done {
		t.Fatal("with the nudge off the turn must end exactly as it did before")
	}

	on := agentAt(true)
	g = turnGates{}
	var notices []string
	emit := func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}
	if done := on.finishTurn(context.Background(), resp, &g, emit); done {
		t.Fatal("a stale green with a claim over it should be sent back")
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "never checked") {
		t.Fatalf("notices = %v", notices)
	}
	last := on.Messages[len(on.Messages)-1]
	if !strings.Contains(last.Text(), "edited again afterwards") {
		t.Fatalf("the model was not told: %q", last.Text())
	}
	// Once a turn: the budget is shared with the overclaim correction, so
	// a second pass over the same turn ends it.
	if done := on.finishTurn(context.Background(), resp, &g, func(Event) {}); !done {
		t.Fatal("the nudge must fire at most once in a turn")
	}
}
