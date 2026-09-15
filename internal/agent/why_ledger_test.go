package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/critic"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// TestNoticesReachTheLedger is the other half: /why can only explain
// what was written down, and until this change most notices were a line
// on the screen and nothing else.
func TestNoticesReachTheLedger(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("done")}}
	a, _ := newAgent(t, f, nil)
	sess := &memLog{}
	a.SetLog(sess)
	a.Prepare = func(b []llm.Block) ([]llm.Block, string) {
		return b, "a note from the harness"
	}
	if err := a.Run(t.Context(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	ivs := critic.Interventions(sess.Events())
	if len(ivs) == 0 {
		t.Fatal("the notice never reached the ledger")
	}
	if ivs[0].Text != "a note from the harness" {
		t.Fatalf("ledger holds %q", ivs[0].Text)
	}
}

// TestARefusalReachesTheLedger: a call that never ran still has to be
// written down, or /why cannot tell a refusal from a call never made.
func TestARefusalReachesTheLedger(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "write_file", `{"path":"a.txt","content":"x"}`),
		text("all right"),
	}}
	a, _ := newAgent(t, f, ApproverFunc(func(context.Context, ToolCall) (Decision, error) {
		return Deny, nil
	}))
	sess := &memLog{}
	a.SetLog(sess)
	if err := a.Run(t.Context(), "write a.txt", nil); err != nil {
		t.Fatal(err)
	}
	ivs := critic.Interventions(sess.Events())
	found := false
	for _, iv := range ivs {
		if iv.Kind == critic.IvRefused && strings.Contains(iv.Text, "write_file") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refusal is not in the ledger; interventions were %v", ivs)
	}
	// And it explains itself in the words a refusal deserves, wherever it
	// sits in the ledger.
	for n := 1; n <= len(ivs); n++ {
		got := critic.WhyLast(sess.Events(), n)
		if strings.HasPrefix(got, string(critic.IvRefused)) {
			if !strings.Contains(got, "not allowed") {
				t.Fatalf("/why did not explain the refusal:\n%s", got)
			}
			return
		}
	}
	t.Fatal("/why never reached the refusal")
}

// memLog is a session log in memory, which is all Interventions needs.
type memLog struct{ events []session.Event }

func (m *memLog) Append(e session.Event) (int, error) {
	e.Seq = len(m.events) + 1
	m.events = append(m.events, e)
	return e.Seq, nil
}
func (m *memLog) Events() []session.Event { return m.events }
