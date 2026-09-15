package agent

import (
	"context"
	"encoding/json"
	"fmt"
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

// Cancelling the turn at a permission prompt is not a refusal of the call:
// the model is told the turn was cancelled, without the denial's advice
// or a "The user said:" wrapped around the harness's own reason.
func TestCancelTellsTheModelTheTurnWasCancelled(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"x"}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, cancelApprover{})
	evs, emit := collect()
	_ = a.Run(context.Background(), "write", emit)
	for _, e := range *evs {
		if e.Kind == EvToolResult {
			if e.Output != CancelledMessage || !e.Denied {
				t.Fatalf("tool result = %q (denied=%v), want %q", e.Output, e.Denied, CancelledMessage)
			}
			return
		}
	}
	t.Fatal("no tool result")
}

// bashRecorder stands in for bash without running anything.
type bashRecorder struct{}

func (bashRecorder) Name() string                                         { return "bash" }
func (bashRecorder) Description() string                                  { return "bash" }
func (bashRecorder) Schema() json.RawMessage                              { return json.RawMessage(`{"type":"object"}`) }
func (bashRecorder) Mutating() bool                                       { return true }
func (bashRecorder) Run(context.Context, json.RawMessage) (string, error) { return "ran", nil }

// The rule a prompt offers is decided once, by the agent, from the command
// with the model's `cd <workspace> &&` taken off: `go test` for that
// shape, nothing at all for a compound command no prefix rule can capture
// — no r option, no ledger shape, and AllowAlways saves nothing. A rule
// saved for `go test` then matches the model's next cd-prefixed call.
func TestPromptRuleIgnoresWorkspaceCdAndRefusesCompoundCommands(t *testing.T) {
	cmd := func(c string) string {
		b, _ := json.Marshal(map[string]string{"command": c})
		return string(b)
	}
	f := &fake{}
	var rules []string
	approver := ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		rules = append(rules, call.Rule)
		return AllowAlways, nil
	})
	a, root := newAgent(t, f, approver)
	a.Root = root
	f.responses = []*llm.Response{
		toolCall("t1", "bash", cmd("cd "+root+" && go test ./internal/x")),
		toolCall("t2", "bash", cmd("cd x && rm -rf y")),
		toolCall("t3", "bash", cmd("cd "+root+" && go test ./...")),
		text("done"),
	}
	a.Tools.Add(bashRecorder{})
	a.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	var saved []string
	a.SaveRule = func(r perm.Rule) error { saved = append(saved, r.String()); return nil }
	if err := a.Run(context.Background(), "run", nil); err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[0] != "bash(go test *)" || rules[1] != "" {
		t.Fatalf("offered rules = %q, want [bash(go test *) \"\"]: the third call is covered by the saved rule", rules)
	}
	if len(saved) != 1 || saved[0] != "bash(go test *)" {
		t.Fatalf("saved = %q", saved)
	}
	if tally := a.Ledger.Get(""); tally.Allowed != 0 {
		t.Fatalf("a call with no rule was recorded under an empty shape: %+v", tally)
	}
}

// rungApprover answers every prompt with AllowAlways, saving the rung at
// pick(call) instead of the one the prompt proposed.
type rungApprover struct {
	pick  func(ToolCall) int
	calls []ToolCall
}

func (r *rungApprover) Approve(ctx context.Context, call ToolCall) (Decision, error) {
	d, _, _, err := r.ApproveWithRule(ctx, call)
	return d, err
}

func (r *rungApprover) ApproveWithReason(ctx context.Context, call ToolCall) (Decision, string, error) {
	d, reason, _, err := r.ApproveWithRule(ctx, call)
	return d, reason, err
}

func (r *rungApprover) ApproveWithRule(ctx context.Context, call ToolCall) (Decision, string, string, error) {
	r.calls = append(r.calls, call)
	i := r.pick(call)
	if i < 0 || i >= len(call.Rungs) {
		return AllowAlways, "", "", nil
	}
	return AllowAlways, "", call.Rungs[i].Rule, nil
}

// TestLedgerCountsTheRungTheUserChose: saving a rung other than the one
// the prompt proposed saves that rule, tallies it under that rule, and
// leaves the default rung uncounted — so the "approved N×" note beside a
// rule always counts that same rule.
func TestLedgerCountsTheRungTheUserChose(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"docs/a.md","content":"1"}`),
		text("done"),
	}}
	ap := &rungApprover{pick: func(ToolCall) int { return 0 }} // the exact path
	a, _ := newAgent(t, f, ap)
	a.Ledger = perm.OpenLedger(t.TempDir(), "proj")
	var saved []string
	a.SaveRule = func(r perm.Rule) error { saved = append(saved, r.String()); return nil }
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if len(ap.calls) != 1 || len(ap.calls[0].Rungs) != 2 || ap.calls[0].RuleIndex != 1 {
		t.Fatalf("prompt = %+v", ap.calls)
	}
	if len(saved) != 1 || saved[0] != "write_file(docs/a.md)" {
		t.Fatalf("saved = %v", saved)
	}
	if tally := a.Ledger.Get("write_file(docs/a.md)"); tally.Allowed != 1 || tally.Denied != 0 {
		t.Fatalf("chosen rung tally = %+v", tally)
	}
	if tally := a.Ledger.Get("write_file(docs/*)"); tally.Allowed != 0 {
		t.Fatalf("the rung the user moved off was counted anyway: %+v", tally)
	}
	// What the ledger counted is what it would go on to propose, once the
	// shape has been approved often enough for /permissions to mention it.
	for i := 1; i < perm.LearnAfter; i++ {
		a.Ledger.Record("write_file(docs/a.md)", true)
	}
	if c := a.Ledger.Candidates(perm.LearnAfter); len(c) != 1 || c[0].Rule != "write_file(docs/a.md)" || c[0].Allowed != perm.LearnAfter {
		t.Fatalf("candidates = %+v", c)
	}
	if lines := perm.CandidateLines(a.Ledger, perm.Rules{}); len(lines) != 1 || lines[0] != fmt.Sprintf("/permissions allow write_file(docs/a.md) (approved %d×, never refused)", perm.LearnAfter) {
		t.Fatalf("candidate lines = %v", lines)
	}
	// It is already saved, so /permissions stops proposing it.
	if lines := perm.CandidateLines(a.Ledger, a.RulesSnapshot()); len(lines) != 0 {
		t.Fatalf("a saved rule is still proposed: %v", lines)
	}
}
