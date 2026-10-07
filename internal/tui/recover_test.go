package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// Opening a session whose last turn died says so, and /recover carries it
// on without a new prompt; a second /recover finds nothing to do.
func TestRecoverCarriesOnTheDeadTurn(t *testing.T) {
	opts := fakeOptions(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "carried on"), StopReason: llm.StopEndTurn})
	user := llm.TextMessage(llm.RoleUser, "do the thing")
	opts.Session.Append(session.Event{Type: session.TypeTurnStart, Turn: 1, Extra: session.TurnLogsEnd})
	opts.Session.Append(session.Event{Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &user})
	opts.Agent.Messages = session.FoldSurface(opts.Session.Events())
	opts.Agent.RestoreFromLog()
	opts.Resumed = len(opts.Agent.Messages)

	h := newHarnessFor(t, opts)
	if v := plain(h.m.View()); !strings.Contains(v, "turn 1 was interrupted") || !strings.Contains(v, "/recover carries it on") {
		t.Fatalf("no notice on opening:\n%s", v)
	}
	h.keys("/recover")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running && strings.Contains(plain(h.m.View()), "carried on") })
	reqs := opts.Agent.Provider.(*scripted).reqs()
	if len(reqs) != 1 || reqs[0].Messages[len(reqs[0].Messages)-1].Text() != "do the thing" {
		t.Fatalf("the continuation did not resend the dead turn as it was: %+v", reqs)
	}
	h.keys("/recover")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "nothing to recover") {
		t.Fatalf("a second /recover:\n%s", v)
	}
}
