package proof

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/evidence"
	"github.com/xmonader/janetcode/internal/session"
)

// The signed evidence reads statuses off the same model as everything
// else: each command carries its status, a gate command that never ran or
// was cancelled is left out (nothing to re-run), and the turn carries the
// final status, which a later edit turns into not_run.
func TestSessionEvidenceCarriesStatuses(t *testing.T) {
	root := "/w/proj"
	green, _ := json.Marshal(GateRun{Exit: 0, Status: string(evidence.Passed)})
	red, _ := json.Marshal(GateRun{Exit: 2, Status: string(evidence.Failed)})
	notRun, _ := json.Marshal(GateRun{Status: string(evidence.NotRun)})
	events := []session.Event{
		{Seq: 1, Type: session.TypeFileChange, Turn: 1, CallID: "c1", Name: "/w/proj/foo.go", Arguments: "a->b"},
		{Seq: 2, Type: session.TypeGate, Turn: 1, Name: "go vet ./...", Source: session.GateVerify, Extra: green},
		{Seq: 3, Type: session.TypeGate, Turn: 1, Name: "go test ./...", Source: session.GateVerify, Extra: red, IsError: true},
		{Seq: 4, Type: session.TypeGate, Turn: 1, Name: "make lint", Source: session.GateVerify, Extra: notRun},
		bashResult(5, 1, "c2", "go build ./...", "dial tcp: lookup proxy.golang.org: no such host\n[exit code 1]"),
	}
	rec := Record{Session: "s1", Model: "m",
		Files: []File{{Path: "foo.go", Changes: []ChangeRef{{Seq: 1, Turn: 1}}}},
		Turns: []Turn{{Turn: 1, Verdict: "check failed"}}}
	se := SessionFromEvents(rec, EvidenceInput{Session: "s1", Model: "m", RepoRoot: root, WorkRoot: root, Events: events})
	tu := se.Turns[0]
	got := map[string]CommandEvidence{}
	for _, c := range tu.Commands {
		got[c.Command] = c
	}
	if _, ok := got["make lint"]; ok {
		t.Errorf("a command the gate never ran is not evidence: %+v", tu.Commands)
	}
	want := map[string]string{"go vet ./...": "passed", "go test ./...": "failed", "go build ./...": "environmental"}
	for cmd, status := range want {
		if got[cmd].Status != status {
			t.Errorf("%s status = %q, want %q", cmd, got[cmd].Status, status)
		}
	}
	if got["go test ./..."].Exit != 2 {
		t.Errorf("the exit code is kept beside the status: %+v", got["go test ./..."])
	}
	if tu.Evidence != string(evidence.Failed) {
		t.Errorf("turn evidence = %q", tu.Evidence)
	}

	// An edit after every check makes them stale: the turn's evidence is
	// not_run, and the commands are marked stale as before.
	events = append(events, session.Event{Seq: 6, Type: session.TypeFileChange, Turn: 1, CallID: "c3", Name: "/w/proj/foo.go", Arguments: "b->c"})
	se = SessionFromEvents(rec, EvidenceInput{Session: "s1", Model: "m", RepoRoot: root, WorkRoot: root, Events: events})
	if se.Turns[0].Evidence != string(evidence.NotRun) || !se.Turns[0].Commands[0].Stale {
		t.Errorf("after a later edit: %+v", se.Turns[0])
	}
}

// The proof note's turn line shows the evidence status beside the verdict.
func TestSummaryShowsTheTurnsEvidence(t *testing.T) {
	rec := Record{Session: "s1", Model: "m",
		Files: []File{{Path: "foo.go", Changes: []ChangeRef{{Seq: 1, Turn: 1}}}},
		Turns: []Turn{{Turn: 1, Verdict: "verified", Why: "ok", Evidence: &evidence.Report{
			Status: evidence.NotRun, Required: true, Blocks: true, Stale: true, Why: "every check predates the last edit (foo.go)",
			Checks: []evidence.Check{{Command: "go test ./...", Status: evidence.Passed}}}}}}
	got := Summary(rec)
	if !strings.Contains(got, "evidence: not_run (stale)") || !strings.Contains(got, "not backed") {
		t.Errorf("summary:\n%s", got)
	}
}
