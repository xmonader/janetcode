package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/blindtest"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// blindReply is a tester's reply with three checks: one that holds, one
// that finds a violation, and one that crashes without the marker.
const blindReply = "RULE: limit must be between 1 and 100\n```sh\ncheck-limit\n```\n" +
	"RULE: max_price filters inclusively\n```sh\ncheck-price\n```\n" +
	"RULE: tags are lower-cased\n```sh\ncheck-tags\n```\n"

// blindRuns answers check runs by command; a missing one is not run.
type blindRuns struct {
	table map[string]probeResult
	runs  []string
}

func (f *blindRuns) exec(_ context.Context, cmd string) probeResult {
	f.runs = append(f.runs, cmd)
	_, body, _ := strings.Cut(cmd, "\n") // after the cd into the copy
	return f.table[body]
}

// blindAgent is an agent whose turn 1 changed one file and created
// another, in a project holding a BRIEF.md, with a tester on its own
// model and a throwaway copy that is a fixed directory.
func blindAgent(t *testing.T, f *fake, runs *blindRuns) (*Agent, string) {
	t.Helper()
	a, root := newAgent(t, f, nil)
	a.Root = root
	write := func(name, body string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("BRIEF.md", "Implement search(limit, max_price).\n")
	write("NOTES.md", "my design: secret-implementation-detail\n")
	write("catalog.py", "def search(): pass  # secret-code\n")
	created := write("helper.py", "x = 1\n")
	a.turnN = 1
	a.record(Change{Turn: 1, Path: filepath.Join(root, "catalog.py"), Existed: true, Before: []byte("old\n"), After: []byte("def search(): pass  # secret-code\n")})
	a.record(Change{Turn: 1, Path: filepath.Join(root, "NOTES.md"), Existed: true, Before: []byte("old\n"), After: []byte("my design: secret-implementation-detail\n")})
	a.record(Change{Turn: 1, Path: created, After: []byte("x = 1\n")})
	a.integ.userText = "Read BRIEF.md and implement the task."
	a.Messages = []llm.Message{
		llm.TextMessage(llm.RoleUser, "Read BRIEF.md and implement the task."),
		llm.TextMessage(llm.RoleAssistant, "transcript-only reasoning"),
	}
	a.Log = &session.Memory{}
	a.probeExec = runs.exec
	a.BlindTest = &BlindTester{Model: "cheap-model", Scratch: func(context.Context, string) (string, func(), error) {
		return "/scratch", func() {}, nil
	}}
	return a, root
}

func blindRunsTable() *blindRuns {
	return &blindRuns{table: map[string]probeResult{
		"check-limit": {outcome: probePass},
		"check-price": {outcome: probeFail, output: "VIOLATION: max_price=4500 excluded P0002\n[exit code 1]"},
		"check-tags":  {outcome: probeFail, output: "NameError: name 'serch' is not defined\n[exit code 1]"},
	}}
}

var greenTurn = TurnOutcome{Verdict: VerdictVerified}

// The tester sees the task and the file names the task found, never the
// code, the transcript, or a doc the turn wrote; the implementer is told
// the failed rule and nothing of the check; the crashed check is not
// reported.
func TestBlindTesterSeesOnlyTheSpecAndReportsOnlyRules(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(blindReply)}}
	runs := blindRunsTable()
	a, _ := blindAgent(t, f, runs)
	var g blindGateState
	if !a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {}) {
		t.Fatal("a failed rule did not send the turn back")
	}
	if len(f.requests) != 1 {
		t.Fatalf("model calls = %d, want 1", len(f.requests))
	}
	req := f.requests[0]
	if req.Model != "cheap-model" || req.System != blindtest.System || len(req.Tools) != 0 {
		t.Fatalf("request: model %q, tools %d, system set %v", req.Model, len(req.Tools), req.System == blindtest.System)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("the tester got %d messages, want the one prompt", len(req.Messages))
	}
	sent := req.Messages[0].Text()
	for _, want := range []string{"Read BRIEF.md", "Implement search(limit, max_price).", "catalog.py", "BRIEF.md"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the prompt is missing %q:\n%s", want, sent)
		}
	}
	for _, leak := range []string{"secret-code", "secret-implementation-detail", "transcript-only", "helper.py"} {
		if strings.Contains(sent, leak) {
			t.Errorf("the prompt leaked %q:\n%s", leak, sent)
		}
	}
	if len(runs.runs) != 3 || !strings.HasPrefix(runs.runs[0], "cd '/scratch'") {
		t.Fatalf("runs = %q, want three in the throwaway copy", runs.runs)
	}
	note := a.Messages[len(a.Messages)-1].Text()
	if !strings.Contains(note, "max_price filters inclusively") {
		t.Fatalf("the note does not name the failed rule:\n%s", note)
	}
	for _, leak := range []string{"check-price", "VIOLATION", "P0002", "tags are lower-cased", "limit must be"} {
		if strings.Contains(note, leak) {
			t.Errorf("the note leaked %q:\n%s", leak, note)
		}
	}
	if g.rec.Checks != 3 || g.rec.Held != 1 || g.rec.Inconclusive != 1 || len(g.rec.Violated) != 1 || !g.rec.SentBack {
		t.Fatalf("record = %+v", g.rec)
	}
	// Once a turn: the next end of the turn reruns the failed check
	// only, with no model call, and records that it now holds.
	runs.table["check-price"] = probeResult{outcome: probePass}
	runs.runs = nil
	if a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {}) {
		t.Fatal("the recheck sent the turn back")
	}
	if len(f.requests) != 1 || len(runs.runs) != 1 || !strings.HasSuffix(runs.runs[0], "check-price") {
		t.Fatalf("recheck: calls %d, runs %q", len(f.requests), runs.runs)
	}
	got := foldBlindTest(a.Log.Events(), 1)
	if got == nil || !got.Rechecked || len(got.Still) != 0 || got.Model != "cheap-model" {
		t.Fatalf("logged record = %+v", got)
	}
	// And never again in that turn.
	if a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {}) || len(runs.runs) != 1 {
		t.Fatal("the tester ran a third time")
	}
}

// A check still failing after the round is recorded as such.
func TestBlindTesterRecordsWhatTheRoundLeftFailing(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(blindReply)}}
	runs := blindRunsTable()
	a, _ := blindAgent(t, f, runs)
	var g blindGateState
	a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {})
	a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {})
	got := foldBlindTest(a.Log.Events(), 1)
	if got == nil || len(got.Still) != 1 || got.Still[0] != "max_price filters inclusively" {
		t.Fatalf("logged record = %+v", got)
	}
	if !strings.Contains(got.Line(), "1 still failing") {
		t.Fatalf("line = %q", got.Line())
	}
}

// Checks that hold, or only crash, send nothing back.
func TestBlindTesterHeldOrCrashedChecksDoNotSendBack(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(blindReply)}}
	runs := blindRunsTable()
	runs.table["check-price"] = probeResult{outcome: probePass}
	a, _ := blindAgent(t, f, runs)
	n := len(a.Messages)
	var g blindGateState
	if a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {}) {
		t.Fatal("sent back with no rule violated")
	}
	if len(a.Messages) != n {
		t.Fatal("a note was added")
	}
	if g.rec.Held != 2 || g.rec.Inconclusive != 1 || g.rec.SentBack {
		t.Fatalf("record = %+v", g.rec)
	}
	// Nothing was sent back, so there is nothing to recheck.
	runs.runs = nil
	a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {})
	if len(runs.runs) != 0 {
		t.Fatalf("rechecked = %q", runs.runs)
	}
}

// Off, a turn that is not green, a turn that changed nothing and a
// read-only one make no call.
func TestBlindTesterRunsOnlyOnAGreenChangingTurn(t *testing.T) {
	for name, tc := range map[string]struct {
		outcome TurnOutcome
		setup   func(a *Agent)
	}{
		"off switch": {greenTurn, func(a *Agent) { a.Off = map[string]bool{"blindtest": true} }},
		"not set":    {greenTurn, func(a *Agent) { a.BlindTest = nil }},
		"red":        {TurnOutcome{Verdict: VerdictCheckFailed}, func(*Agent) {}},
		"no changes": {TurnOutcome{Verdict: VerdictNoChanges}, func(*Agent) {}},
		"unverified": {TurnOutcome{Verdict: VerdictUnverified}, func(*Agent) {}},
		"read-only":  {greenTurn, func(a *Agent) { a.ReadOnly = true }},
		"dry run":    {greenTurn, func(a *Agent) { a.SetDryRun(true) }},
	} {
		f := &fake{responses: []*llm.Response{text(blindReply)}}
		a, _ := blindAgent(t, f, blindRunsTable())
		tc.setup(a)
		var g blindGateState
		if a.blindTestGate(context.Background(), tc.outcome, &g, func(Event) {}) || len(f.requests) != 0 {
			t.Errorf("%s: sent back or called the model (%d calls)", name, len(f.requests))
		}
	}
}

// No checks in the reply, a failed call, or no throwaway copy: skipped
// and recorded, never sent back.
func TestBlindTesterSkipsAndRecordsWhy(t *testing.T) {
	cases := map[string]struct {
		reply   string
		scratch bool
		want    string
	}{
		"no checks":  {"I cannot tell what to test.", true, "no checks"},
		"no copy":    {blindReply, false, "no throwaway copy"},
		"empty body": {"RULE: x\n```sh\n```\n", true, "no checks"},
	}
	for name, tc := range cases {
		f := &fake{responses: []*llm.Response{text(tc.reply)}}
		runs := blindRunsTable()
		a, _ := blindAgent(t, f, runs)
		if !tc.scratch {
			a.BlindTest.Scratch = nil
		}
		var g blindGateState
		if a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {}) {
			t.Errorf("%s: sent back", name)
		}
		got := foldBlindTest(a.Log.Events(), 1)
		if got == nil || !strings.Contains(got.Skipped, tc.want) || len(runs.runs) != 0 {
			t.Errorf("%s: record %+v, runs %q", name, got, runs.runs)
		}
	}
}

// An empty slot model is the session's own, and the call goes through
// the route like any side call.
func TestBlindTesterOnTheSessionModel(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("no rules here")}}
	a, _ := blindAgent(t, f, blindRunsTable())
	a.BlindTest.Model = ""
	var g blindGateState
	a.blindTestGate(context.Background(), greenTurn, &g, func(Event) {})
	if len(f.requests) != 1 || f.requests[0].Model != "m" || g.rec.Model != "m" {
		t.Fatalf("calls %d, model %q, record %q", len(f.requests), f.requests[0].Model, g.rec.Model)
	}
}
