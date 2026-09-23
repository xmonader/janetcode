package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

// decisionStatuses returns the status field of every recorded decision
// event for one consumer, in log order.
func decisionStatuses(t *testing.T, mem *session.Memory, consumer string) []string {
	t.Helper()
	var out []string
	for _, e := range mem.Events() {
		if e.Type != session.TypeDecision || e.Name != consumer {
			continue
		}
		var rec struct {
			Status  string `json:"status"`
			ActedOn bool   `json:"acted_on"`
		}
		if err := json.Unmarshal(e.Extra, &rec); err != nil {
			t.Fatalf("decision record does not parse: %v", err)
		}
		if rec.ActedOn {
			t.Fatalf("a failed %s decision must record acted_on false", consumer)
		}
		out = append(out, rec.Status)
	}
	return out
}

// A failing triage call changes nothing (TestTriageFailsOpen owns that
// contract) and is still recorded: the session log, not the transcript,
// is where a skipped advisory shows up afterwards.
func TestTriageFailureIsRecorded(t *testing.T) {
	run := func(fd DecisionProvider) []string {
		f := &fake{responses: []*llm.Response{
			toolCall("t1", "write_file", `{"path":"a.txt","content":"v1"}`),
			text("done"),
			toolCall("t2", "write_file", `{"path":"a.txt","content":"ok"}`),
			text("fixed"),
		}}
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
		return decisionStatuses(t, mem, "verify_triage")
	}

	if got := run(&fakeDecisions{err: errors.New("boom"), avail: true}); len(got) != 1 || got[0] != "error" {
		t.Fatalf("a provider error records status error, got %v", got)
	}
	// The real circuit opens on the failing call itself, so the error and
	// the open arrive together; a fake that starts unavailable never
	// reaches the call (triageFailure refuses before asking).
	if got := run(&trippingDecisions{avail: true}); len(got) != 1 || got[0] != "circuit_open" {
		t.Fatalf("an error that trips the circuit records circuit_open, got %v", got)
	}
}

// trippingDecisions fails the way the real client does when its circuit
// opens: Available() flips inside the failing call.
type trippingDecisions struct{ avail bool }

func (f *trippingDecisions) Evaluate(_ context.Context, _ string, _ []jev.Question) (*jev.Result, error) {
	f.avail = false
	return nil, errors.New("boom")
}
func (f *trippingDecisions) Available() bool { return f.avail }

// An intake classification that misses even the handoff slack is a
// decision call the session never got an answer to: recorded as a
// timeout, never an error, and never acted on.
func TestIntakeTimeoutIsRecorded(t *testing.T) {
	mem := &session.Memory{}
	a := &Agent{Log: mem, turnN: 1}
	a.intakeCh = make(chan intakeOutcome, 1)
	a.intakeDeadline = time.Now().Add(15 * time.Millisecond)
	a.drainIntake()
	got := decisionStatuses(t, mem, "intake")
	if len(got) != 1 || got[0] != "timeout" {
		t.Fatalf("intake timeout = %v", got)
	}
}

// A failing session-outcome call is recorded like the others: visible in
// the log, inert in behaviour.
func TestOutcomeFailureIsRecorded(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("done")}}
	a, _ := newAgent(t, f, nil)
	a.AutoApprove = true
	mem := &session.Memory{}
	a.Log = mem
	if err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	a.Decisions = &fakeDecisions{err: errors.New("boom"), avail: true}
	a.DecisionsSessionOutcome = true
	a.ClassifySessionOutcome(context.Background())
	if got := decisionStatuses(t, mem, "session_outcome"); len(got) != 1 || got[0] != "error" {
		t.Fatalf("outcome failure = %v", got)
	}
}
