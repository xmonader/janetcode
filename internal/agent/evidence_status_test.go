package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/evidence"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

const (
	editA = `{"path":"a.go"}`
	goT   = `{"command":"go test ./..."}`
)

// A success claim is only as good as the evidence under it. The verdict
// can read "verified" (a check ran green at some point) while the
// evidence model says the check is stale, flaky, environmental or
// cancelled: each of those blocks the claim and says why.
func TestModelOverclaimingIsBlockedByEvidenceStatus(t *testing.T) {
	green := "ok  \tx\t0.1s\n"
	cases := []struct {
		name   string
		events []session.Event
		status evidence.Status
		block  bool
	}{
		{"fresh green backs the claim", []session.Event{
			result(1, "edit_file", editA, "ok", false),
			result(1, "bash", goT, green, false),
			say(1, "Done, the tests pass."),
		}, evidence.Passed, false},
		{"stale green", []session.Event{
			result(1, "edit_file", editA, "ok", false),
			result(1, "bash", goT, green, false),
			result(1, "edit_file", `{"path":"b.go"}`, "ok", false),
			say(1, "Done, the tests pass."),
		}, evidence.NotRun, true},
		{"flaky: green on retry with nothing changed", []session.Event{
			result(1, "edit_file", editA, "ok", false),
			result(1, "bash", goT, "--- FAIL: TestRace\nFAIL\n[exit code 1]", true),
			{Type: session.TypeToolResult, Turn: 1, CallID: "second", Name: "bash", Arguments: goT, Output: green},
			say(1, "Done, the tests pass."),
		}, evidence.Flaky, true},
		{"environmental failure", []session.Event{
			result(1, "edit_file", editA, "ok", false),
			result(1, "bash", goT, "dial tcp: lookup proxy.golang.org: no such host\n[exit code 1]", true),
			say(1, "Done, the tests pass."),
		}, evidence.Environmental, true},
		{"cancelled run", []session.Event{
			result(1, "edit_file", editA, "ok", false),
			result(1, "bash", goT, CancelledMessage, true),
			say(1, "Done, the tests pass."),
		}, evidence.Cancelled, true},
	}
	for _, c := range cases {
		o := FoldOutcome(c.events, 1)
		if o.Evidence == nil || o.Evidence.Status != c.status || o.Evidence.Blocks != c.block {
			t.Errorf("%s: evidence = %+v, want %s blocks=%v", c.name, o.Evidence, c.status, c.block)
			continue
		}
		if o.Overclaimed() != c.block {
			t.Errorf("%s: Overclaimed = %v, want %v (verdict %s)", c.name, o.Overclaimed(), c.block, o.Verdict)
		}
		if c.block {
			if corr := o.Correction(); !strings.Contains(corr, "tests pass") || corr == "" {
				t.Errorf("%s: correction %q does not quote the claim", c.name, corr)
			}
			if why := o.OverclaimWhy(); why == "" {
				t.Errorf("%s: empty overclaim reason", c.name)
			}
		}
	}
}

// Without a claim nothing is blocked from speaking: the evidence is
// reported, and the verdict line stays exactly as headless callers parse.
func TestEvidenceReportsWithoutAClaimAndLeavesTheVerdictLineAlone(t *testing.T) {
	o := FoldOutcome([]session.Event{
		result(1, "edit_file", editA, "ok", false),
		result(1, "bash", goT, "ok  \tx\t0.1s\n", false),
		result(1, "edit_file", `{"path":"b.go"}`, "ok", false),
	}, 1)
	if o.Overclaimed() {
		t.Fatal("no claim, nothing to overclaim")
	}
	if !strings.Contains(o.EvidenceLine(), "not_run") || !strings.Contains(o.EvidenceLine(), "stale") {
		t.Errorf("evidence line = %q", o.EvidenceLine())
	}
	if strings.Contains(o.Line(), "evidence") {
		t.Errorf("the verdict line must stay stable: %q", o.Line())
	}
	if ev := verdictEvent(o); ev.Evidence != o.Evidence || ev.Text != o.Line() {
		t.Errorf("event = %+v", ev)
	}
}

// Docs and acceptance-writing turns owe no check.
func TestEvidenceIsNotOwedForDocsOrRedAcceptanceTests(t *testing.T) {
	o := FoldOutcome([]session.Event{result(1, "edit_file", `{"path":"README.md"}`, "ok", false), say(1, "Done, tests pass.")}, 1)
	if o.Evidence.Required || o.Evidence.Blocks || o.Overclaimed() {
		t.Errorf("docs-only: %+v", o.Evidence)
	}
}

// logGate writes each command on the evidence model: status, exit code,
// duration; commands the run never reached are not_run and an interrupted
// one cancelled, and the verdict fold counts neither as a check.
func TestLogGateRecordsStatusesAndTheFoldIgnoresSkipped(t *testing.T) {
	mem := &session.Memory{}
	a := &Agent{Log: mem}
	a.turnN = 1
	mem.Append(session.Event{Type: session.TypeFileChange, Turn: 1, Name: "a.go"})
	mem.Append(result(1, "edit_file", editA, "ok", false))
	a.logGate(session.GateVerify, verify.Result{
		Passed: []string{"go vet ./..."}, Failed: "go test ./...", Exit: 1, Output: "--- FAIL: TestA\nFAIL\n",
		Runs: []verify.Run{
			{Name: "go vet ./...", Exit: 0, Duration: 1500 * time.Millisecond},
			{Name: "go test ./...", Exit: 1, Output: "--- FAIL: TestA\nFAIL\n", Duration: 2 * time.Second},
		},
		NotRun: []string{"make lint"},
	})
	rep := a.EvidenceReport()
	got := map[string]evidence.Check{}
	for _, c := range rep.Checks {
		got[c.Command] = c
	}
	if got["go vet ./..."].Status != evidence.Passed || got["go test ./..."].Status != evidence.Failed || got["make lint"].Status != evidence.NotRun {
		t.Fatalf("checks = %+v", rep.Checks)
	}
	if c := got["go test ./..."]; c.Exit != 1 || !strings.Contains(c.Summary, "FAIL") {
		t.Errorf("failed check = %+v", c)
	}
	if rep.Status != evidence.Failed || !rep.Blocks {
		t.Errorf("report = %+v", rep)
	}
	o := FoldOutcome(mem.Events(), 1)
	if o.Ran != 2 || o.Passed != 1 || o.Failed != 1 || o.Verdict != VerdictCheckFailed {
		t.Errorf("the not_run command must not count as a check: %+v", o)
	}

	// An interrupted gate is cancelled, not failed.
	mem2 := &session.Memory{}
	b := &Agent{Log: mem2}
	b.turnN = 1
	mem2.Append(session.Event{Type: session.TypeFileChange, Turn: 1, Name: "a.go"})
	mem2.Append(result(1, "edit_file", editA, "ok", false))
	b.logGate(session.GateVerify, verify.Result{
		Failed: "sleep 30", Exit: -1, Cancelled: true,
		Runs:   []verify.Run{{Name: "sleep 30", Exit: -1, Cancelled: true}},
		NotRun: []string{"go test ./..."},
	})
	rep = b.EvidenceReport()
	if rep.Status != evidence.Cancelled || !rep.Blocks {
		t.Fatalf("cancelled gate: %+v", rep)
	}
	o = FoldOutcome(mem2.Events(), 1)
	if o.Failed != 0 || o.Verdict == VerdictCheckFailed {
		t.Errorf("a cancelled gate is not a failed check: %+v", o)
	}
}

// A gate that is cancelled mid-run is still recorded (it used to vanish
// from the log), as a cancelled check.
func TestCancelledGateRunIsLoggedNotDropped(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"v1"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	ctx, cancel := context.WithCancel(context.Background())
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"sleep 30"}}}
	mem := &session.Memory{}
	a.Log = mem
	go func() { time.Sleep(700 * time.Millisecond); cancel() }()
	_ = a.Run(ctx, "go", nil)
	rep := a.EvidenceReport()
	if len(rep.Checks) == 0 || rep.Checks[0].Status != evidence.Cancelled {
		t.Fatalf("cancelled gate must be on the log as cancelled: %+v", rep)
	}
}

// The decision service's answer is recorded beside the command's result,
// never in it: confident "environmental" or an outright error, the check
// still reads failed with its own exit code.
func TestJevAdvisoryNeverChangesCommandTruth(t *testing.T) {
	run := func(fd DecisionProvider) evidence.Report {
		// Enough scripted turns for every repair round the gate may buy.
		var script []*llm.Response
		for i := 0; i < 5; i++ {
			script = append(script, toolCall("t"+string(rune('a'+i)), "write_file", `{"path":"a.txt","content":"v`+string(rune('1'+i))+`"}`), text("done"))
		}
		f := &fake{responses: script}
		a, root := newAgent(t, f, nil)
		a.AutoApprove = true
		a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"grep -q ok a.txt"}}}
		a.Decisions = fd
		a.DecisionsTriage = true
		mem := &session.Memory{}
		a.Log = mem
		if err := a.Run(context.Background(), "go", nil); err != nil {
			t.Fatal(err)
		}
		// The report as it stood when the service first answered: later
		// repair rounds are other runs of the command on other trees.
		evs := mem.Events()
		for i, e := range evs {
			if e.Type == session.TypeDecision {
				return evidence.FromEvents(evs[:i+1], evidenceOptions(0, ""))
			}
		}
		t.Fatal("no decision was recorded")
		return evidence.Report{}
	}
	check := func(rep evidence.Report) evidence.Check {
		t.Helper()
		for _, c := range rep.Checks {
			if c.Command == "grep -q ok a.txt" {
				return c
			}
		}
		t.Fatalf("no gate check in %+v", rep)
		return evidence.Check{}
	}

	c := check(run(&fakeDecisions{res: triageResult(0.1, "environmental", 0.9), avail: true}))
	if c.Status != evidence.Failed || c.Exit == 0 {
		t.Fatalf("the exit code decides: %+v", c)
	}
	if c.Advisory == nil || c.Advisory.Class != "environmental" || !c.Advisory.Acted || c.Advisory.Source != "jev" {
		t.Fatalf("the advice is recorded beside the check: %+v", c.Advisory)
	}

	c = check(run(&fakeDecisions{err: errors.New("boom"), avail: true}))
	if c.Status != evidence.Failed || c.Exit == 0 {
		t.Fatalf("a jev error must not touch the result: %+v", c)
	}
	if c.Advisory == nil || c.Advisory.Error != "error" || c.Advisory.Class != "" || c.Advisory.Acted {
		t.Fatalf("a jev error is recorded as no answer: %+v", c.Advisory)
	}
}
