package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// result builds one tool/result event of a turn.
func result(turn int, name, args, out string, isErr bool) session.Event {
	return session.Event{Type: session.TypeToolResult, Turn: turn, CallID: "c" + name + args, Name: name, Arguments: args, Output: out, IsError: isErr}
}

func say(turn int, text string) session.Event {
	m := llm.TextMessage(llm.RoleAssistant, text)
	return session.Event{Type: session.TypeAssistant, Turn: turn, Surface: true, Message: &m}
}

func TestVerdicts(t *testing.T) {
	edit := `{"path":"a.go","old_str":"x","new_str":"y"}`
	test := `{"command":"go test ./..."}`

	cases := []struct {
		name   string
		events []session.Event
		want   Verdict
	}{
		{"nothing happened", nil, VerdictNoChanges},
		{"only read", []session.Event{result(1, "read_file", `{"path":"a.go"}`, "1\tx", false)}, VerdictNoChanges},
		{"edited, ran nothing", []session.Event{result(1, "edit_file", edit, "ok", false)}, VerdictUnverified},
		{"edited, check red", []session.Event{
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", test, "FAIL\n[exit code 1]", true),
		}, VerdictCheckFailed},
		{"edited, check green but never was red", []session.Event{
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", test, "ok  example.com/x\n[exit code 0]", false),
		}, VerdictNotDemonstrated},
		{"edited, check went red to green", []session.Event{
			result(0, "bash", test, "FAIL\n[exit code 1]", true),
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", test, "ok  example.com/x\n[exit code 0]", false),
		}, VerdictVerified},
		{"green that collected nothing", []session.Event{
			result(1, "edit_file", edit, "ok", false),
			result(1, "bash", test, "no tests to run\n[exit code 0]", false),
		}, VerdictVacuous},
		{"check ran but nothing was edited", []session.Event{
			result(1, "bash", test, "ok\n[exit code 0]", false),
		}, VerdictNoChanges},
	}
	for _, c := range cases {
		got := FoldOutcome(c.events, 1)
		if got.Verdict != c.want {
			t.Fatalf("%s: verdict %q, want %q (%s)", c.name, got.Verdict, c.want, got.Why)
		}
		if got.Why == "" {
			t.Fatalf("%s: no reason given", c.name)
		}
	}
}

// A red-to-green flip must be the same command; fixing one thing while a
// different check happens to pass is not a demonstration.
func TestVerifiedNeedsTheSameCommandToFlip(t *testing.T) {
	ev := []session.Event{
		result(0, "bash", `{"command":"go test ./parser"}`, "FAIL\n[exit code 1]", true),
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"go build ./..."}`, "\n[exit code 0]", false),
	}
	if got := FoldOutcome(ev, 1); got.Verdict != VerdictNotDemonstrated {
		t.Fatalf("verdict = %q (%s)", got.Verdict, got.Why)
	}
}

// Distinct files are counted once each, however many times they are written.
func TestEditedCountsFilesNotCalls(t *testing.T) {
	ev := []session.Event{
		result(1, "edit_file", `{"path":"a.go","old_str":"1"}`, "ok", false),
		result(1, "edit_file", `{"path":"a.go","old_str":"2"}`, "ok", false),
		result(1, "write_file", `{"path":"b.go"}`, "ok", false),
		result(1, "edit_file", `{"path":"c.go"}`, "failed", true),
	}
	got := FoldOutcome(ev, 1)
	if got.Edited != 2 {
		t.Fatalf("edited = %d, want the two files that were written", got.Edited)
	}
}

func TestOverclaimAndCorrection(t *testing.T) {
	edited := []session.Event{result(1, "edit_file", `{"path":"a.go"}`, "ok", false)}
	// A claim the turn does not support.
	o := FoldOutcome(append(edited, say(1, "Fixed it — all tests pass now.")), 1)
	if o.Verdict != VerdictUnverified || !o.Overclaimed() {
		t.Fatalf("outcome = %+v", o)
	}
	if c := o.Correction(); !strings.Contains(c, "all tests pass") || !strings.Contains(c, "Run the project's tests") {
		t.Fatalf("correction = %q", c)
	}
	// The same state without the claim is reported, not corrected.
	quiet := FoldOutcome(append(edited, say(1, "I changed the parser.")), 1)
	if quiet.Overclaimed() {
		t.Fatalf("a plain answer was treated as a claim: %+v", quiet)
	}
	// A claim that the turn does support is not corrected either.
	ok := FoldOutcome([]session.Event{
		result(0, "bash", `{"command":"go test ./..."}`, "FAIL\n[exit code 1]", true),
		result(1, "edit_file", `{"path":"a.go"}`, "ok", false),
		result(1, "bash", `{"command":"go test ./..."}`, "ok\n[exit code 0]", false),
		say(1, "The tests pass now."),
	}, 1)
	if ok.Verdict != VerdictVerified || ok.Overclaimed() {
		t.Fatalf("a supported claim was corrected: %+v", ok)
	}
}

// The loop puts an unsupported claim back to the model once, and the
// verdict lands on the log either way.
func TestTurnCorrectsAnOverclaimOnce(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.go","content":"package a\n"}`),
		text("Done — all tests pass."),
		text("You are right; I have not run them."),
	}}
	mem := &session.Memory{}
	a := &Agent{Provider: f, Model: "m", Log: mem, AutoApprove: true,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root})}
	var notices []string
	if err := a.Run(context.Background(), "add a.go", func(e Event) {
		if e.Kind == EvNotice || e.Kind == EvVerdict {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("%d model calls; the claim should have been put back once", len(f.requests))
	}
	joined := strings.Join(notices, " | ")
	if !strings.Contains(joined, "claims more than this turn shows") {
		t.Fatalf("no correction notice: %s", joined)
	}
	if !strings.Contains(joined, "unverified") {
		t.Fatalf("no verdict reported: %s", joined)
	}
	// The verdict is on the log, so it can be read back.
	var found bool
	for _, e := range mem.Events() {
		if e.Type == session.TypeVerdict {
			found = true
			if e.Name != string(VerdictUnverified) || !e.IsError {
				t.Fatalf("logged verdict = %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("the verdict was not logged")
	}
	// Switched off, nothing is corrected.
	g := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"b.go","content":"package b\n"}`),
		text("Done — all tests pass."),
	}}
	b := &Agent{Provider: g, Model: "m", Log: &session.Memory{}, AutoApprove: true,
		Off: map[string]bool{"verdict": true}, Tools: tool.NewRegistry(tool.WriteFile{Root: root})}
	if err := b.Run(context.Background(), "add b.go", nil); err != nil {
		t.Fatal(err)
	}
	if len(g.requests) != 2 {
		t.Fatalf("%d model calls with the verdict off", len(g.requests))
	}
}
