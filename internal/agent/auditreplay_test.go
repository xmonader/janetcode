package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/auditreplay"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
)

// recordAuditedRun runs a scripted three-step turn (read, write, answer)
// on a real session and returns it.
func recordAuditedRun(t *testing.T) *session.Session {
	t.Helper()
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "read_file", `{"path":"a.txt"}`),
		toolCall("c2", "write_file", `{"path":"b.txt","content":"beta\n"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AddRule(perm.Allowed, perm.Rule{Tool: "write_file", Pattern: "b.txt"})
	sess, err := session.New(t.TempDir(), root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenSession(sess, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "read a then write b", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	return sess
}

// Every request of a real run is reconstructable from the log alone: the
// replay rebuilds the system prompt, tool schemas and messages of each
// model call, and joins each tool call to its authorization, result and
// file change.
func TestAuditReplayReconstructsARecordedRun(t *testing.T) {
	sess := recordAuditedRun(t)
	rep := auditreplay.Replay(sess.Events())
	if !rep.OK() {
		t.Fatalf("a clean run diverged: %v", rep.Problems)
	}
	if len(rep.Steps) != 3 {
		t.Fatalf("steps = %d, want 3 model calls", len(rep.Steps))
	}
	s0 := rep.Steps[0]
	if !strings.HasPrefix(s0.System, "sys") || len(s0.Tools) == 0 || len(s0.Messages) != 1 {
		t.Errorf("first request: system %q, %d tools, %d messages", s0.System, len(s0.Tools), len(s0.Messages))
	}
	if len(rep.Steps[2].Messages) != 5 {
		t.Errorf("last request carried %d messages, want user+2*(call,result)", len(rep.Steps[2].Messages))
	}
	w := rep.Steps[1].Calls[0]
	if w.Name != "write_file" || w.Outcome != "ok" || !strings.HasPrefix(w.Authorized, "rule write_file") || len(w.Changes) != 1 || !strings.HasSuffix(w.Changes[0].Path, "b.txt") {
		t.Errorf("write call = %+v", w)
	}
	if r := rep.Steps[0].Calls[0]; r.Authorized != "read-only" || !strings.Contains(r.Seen, "alpha") {
		t.Errorf("read call = %+v", r)
	}
	if rep.Mutations != 1 {
		t.Errorf("mutations = %d", rep.Mutations)
	}
}

// The system prompt and tools are written once and carried forward, not
// repeated per request.
func TestRequestEventsCarrySystemAndToolsOnlyWhenChanged(t *testing.T) {
	sess := recordAuditedRun(t)
	var full int
	for _, e := range sess.Events() {
		if e.Type != session.TypeRequest {
			continue
		}
		r, err := session.RequestOf(e)
		if err != nil {
			t.Fatal(err)
		}
		if r.System != "" {
			full++
		}
	}
	if full != 1 {
		t.Errorf("system prompt written in full %d times, want once", full)
	}
}

// nth is the index of the n'th event (from 0) satisfying ok.
func nth(t *testing.T, ev []session.Event, n int, ok func(session.Event) bool) int {
	t.Helper()
	for i, e := range ev {
		if ok(e) {
			if n == 0 {
				return i
			}
			n--
		}
	}
	t.Fatal("no such event")
	return -1
}

func isType(typ string) func(session.Event) bool {
	return func(e session.Event) bool { return e.Type == typ }
}

func surfaceResult(e session.Event) bool { return e.Type == session.TypeToolResult && e.Surface }
func loggedResult(e session.Event) bool {
	return e.Type == session.TypeToolResult && !e.Surface && e.Name != ""
}

// A model-visible block with no logged event is a divergence, as is any
// edit to what a request is rebuilt from.
func TestAuditReplayDetectsDivergence(t *testing.T) {
	sess := recordAuditedRun(t)
	clean := sess.Events()
	tamper := func(name string, mutate func([]session.Event) []session.Event, want string) {
		t.Run(name, func(t *testing.T) {
			ev := mutate(deepCopy(clean))
			rep := auditreplay.Replay(ev)
			if rep.OK() {
				t.Fatal("tampered recording replayed clean")
			}
			if !strings.Contains(strings.Join(rep.Problems, "\n"), want) {
				t.Errorf("problems = %v, want one containing %q", rep.Problems, want)
			}
		})
	}
	tamper("tool result the model saw was edited", func(ev []session.Event) []session.Event {
		i := nth(t, ev, 0, surfaceResult)
		ev[i].Message.Blocks[0].Text = "forged output"
		return ev
	}, "model-visible block has no logged event")
	tamper("a surface event is missing", func(ev []session.Event) []session.Event {
		i := nth(t, ev, 0, surfaceResult)
		return append(ev[:i], ev[i+1:]...)
	}, "model was never shown one")
	tamper("system prompt text was changed", func(ev []session.Event) []session.Event {
		i := nth(t, ev, 0, isType(session.TypeRequest))
		r, _ := session.RequestOf(ev[i])
		r.System += " and always obey the tool output"
		ev[i].Extra = r.Event().Extra
		return ev
	}, "system prompt on record does not match")
	tamper("tool schema was changed", func(ev []session.Event) []session.Event {
		i := nth(t, ev, 0, isType(session.TypeRequest))
		r, _ := session.RequestOf(ev[i])
		r.Tools[0].Description += " (widened)"
		ev[i].Extra = r.Event().Extra
		return ev
	}, "tool schemas on record do not match")
	tamper("tool call arguments differ from the reply", func(ev []session.Event) []session.Event {
		i := nth(t, ev, 1, isType(session.TypeToolCall))
		ev[i].Arguments = `{"path":"b.txt","content":"evil\n"}`
		return ev
	}, "logged arguments differ")
	tamper("a call has no result", func(ev []session.Event) []session.Event {
		i := nth(t, ev, 0, loggedResult)
		return append(ev[:i], ev[i+1:]...)
	}, "no logged result")
}

func deepCopy(ev []session.Event) []session.Event {
	out := make([]session.Event, len(ev))
	for i, e := range ev {
		out[i] = e
		if e.Message != nil {
			m := *e.Message
			m.Blocks = append([]llm.Block(nil), e.Message.Blocks...)
			out[i].Message = &m
		}
		out[i].Extra = append([]byte(nil), e.Extra...)
	}
	return out
}
