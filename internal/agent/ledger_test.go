package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
)

// bashLikeWarner stands in for the real bash tool (not registered by
// newAgent) with a Warn that always flags: enough to exercise the rule
// that a warned call never carries the learned note.
type bashLikeWarner struct{}

func (bashLikeWarner) Name() string                                         { return "bash" }
func (bashLikeWarner) Description() string                                  { return "bash" }
func (bashLikeWarner) Schema() json.RawMessage                              { return json.RawMessage(`{"type":"object"}`) }
func (bashLikeWarner) Mutating() bool                                       { return true }
func (bashLikeWarner) Run(context.Context, json.RawMessage) (string, error) { return "ran", nil }
func (bashLikeWarner) Warn(json.RawMessage) string                          { return "destructive command" }

// TestLedgerRecordsApprovalsAndRefusals checks the two halves of the
// authorize path this feature adds: every prompt answer lands in the
// ledger under the rule shape the r key would save, and Stats counts the
// prompts shown and how many were refused.
func TestLedgerRecordsApprovalsAndRefusals(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"1"}`),
		toolCall("t2", "write_file", `{"path":"y","content":"2"}`),
		text("done"),
	}}
	decisions := []Decision{Allow, Deny}
	i := 0
	approver := ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		d := decisions[i]
		i++
		return d, nil
	})
	a, _ := newAgent(t, f, approver)
	a.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	shape := perm.Suggest("write_file", "x").String() // write_file's shape is a bare dir-less path here
	// Both calls share the same shape: write_file with no directory in
	// the path resolves to the bare tool, per perm.Suggest.
	tally := a.Ledger.Get(shape)
	if tally.Allowed != 1 || tally.Denied != 1 {
		t.Fatalf("tally = %+v", tally)
	}
	st := a.Stats()
	if st.Approvals != 2 || st.Refusals != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if line := st.PromptLine(); line != "asked 2 times · 1 refused" {
		t.Fatalf("PromptLine = %q", line)
	}
}

// TestLearnedNoteAppearsAfterThreeCleanApprovals: the fourth prompt for a
// shape approved three times running, with no refusal, carries the
// Learned note; the first three do not, because the tally has not
// reached perm.LearnAfter yet.
func TestLearnedNoteAppearsAfterThreeCleanApprovals(t *testing.T) {
	var responses []*llm.Response
	for i := 0; i < perm.LearnAfter+1; i++ {
		responses = append(responses, toolCall("t", "write_file", `{"path":"x","content":"1"}`))
	}
	responses = append(responses, text("done"))
	f := &fake{responses: responses}
	var seen []string
	approver := ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		seen = append(seen, call.Learned)
		return Allow, nil
	})
	a, _ := newAgent(t, f, approver)
	a.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != perm.LearnAfter+1 {
		t.Fatalf("prompts shown = %d, want %d", len(seen), perm.LearnAfter+1)
	}
	for i, learned := range seen[:perm.LearnAfter] {
		if learned != "" {
			t.Fatalf("prompt %d already carried a learned note: %q", i, learned)
		}
	}
	if last := seen[perm.LearnAfter]; last == "" {
		t.Fatal("the prompt after three clean approvals had no learned note")
	}
}

// TestRefusalResetsTheLearnedNote: once a shape has been refused, it is
// never marked learned again even after further approvals, because
// Candidates and the Learned check both require Denied == 0.
func TestRefusalResetsTheLearnedNote(t *testing.T) {
	var responses []*llm.Response
	for i := 0; i < perm.LearnAfter; i++ {
		responses = append(responses, toolCall("t", "write_file", `{"path":"x","content":"1"}`))
	}
	responses = append(responses, toolCall("t", "write_file", `{"path":"x","content":"1"}`))
	for i := 0; i < perm.LearnAfter+1; i++ {
		responses = append(responses, toolCall("t", "write_file", `{"path":"x","content":"1"}`))
	}
	responses = append(responses, text("done"))
	f := &fake{responses: responses}
	call := 0
	var seen []string
	approver := ApproverFunc(func(ctx context.Context, c ToolCall) (Decision, error) {
		seen = append(seen, c.Learned)
		call++
		if call == perm.LearnAfter+1 {
			return Deny, nil
		}
		return Allow, nil
	})
	a, _ := newAgent(t, f, approver)
	a.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	for i, learned := range seen {
		if i == perm.LearnAfter {
			continue // the denied call itself; irrelevant here
		}
		if i > perm.LearnAfter && learned != "" {
			t.Fatalf("prompt %d after a refusal still carried a learned note: %q", i, learned)
		}
	}
}

// TestLearnedNoteHiddenBehindAWarning: a call the tool itself flags as
// risky never gets the reassuring "approved before" note, whatever the
// ledger says.
func TestLearnedNoteHiddenBehindAWarning(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"rm -rf /tmp/x"}`),
		text("done"),
	}}
	var got string
	approver := ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		got = call.Learned
		return Allow, nil
	})
	a, root := newAgent(t, f, approver)
	a.Tools.Add(bashLikeWarner{})
	_ = root
	a.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	shape := perm.Suggest("bash", "rm -rf /tmp/x").String()
	for i := 0; i < perm.LearnAfter; i++ {
		a.Ledger.Record(shape, true)
	}
	if err := a.Run(context.Background(), "run", nil); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("learned note shown despite a warning: %q", got)
	}
}

type cancelApprover struct{}

func (cancelApprover) Approve(ctx context.Context, call ToolCall) (Decision, error) { return Deny, nil }
func (cancelApprover) ApproveWithReason(ctx context.Context, call ToolCall) (Decision, string, error) {
	return Deny, CancelledReason, nil
}

func TestCancelIsNotRecordedAsRefusal(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"x"}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, cancelApprover{})
	a.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	_ = a.Run(context.Background(), "write", nil)
	shape := perm.Suggest("write_file", perm.Subject("write_file", []byte(`{"path":"a.txt"}`))).String()
	if got := a.Ledger.Get(shape); got.Denied != 0 || got.Allowed != 0 {
		t.Fatalf("cancel recorded in the ledger as %+v", got)
	}
}
