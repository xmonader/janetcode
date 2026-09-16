package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/verify"
)

// The session log is the only record a resume rebuilds the conversation
// from, so anything the turn adds to Messages has to reach it as a
// surface event. A path that appends to the live history without logging
// it folds to a different — and usually provider-invalid — conversation:
// two assistant turns in a row, or a tool_use with no tool_result.
func assertLogFoldsToHistory(t *testing.T, a *Agent, mem *session.Memory) {
	t.Helper()
	got := session.FoldSurface(mem.Events())
	want := a.Messages
	dump := func(msgs []llm.Message) string {
		b, _ := json.Marshal(msgs)
		return string(b)
	}
	if len(got) != len(want) {
		t.Fatalf("the log folds to %d messages, the live history has %d\nlog:  %s\nlive: %s",
			len(got), len(want), dump(got), dump(want))
	}
	for i := range want {
		if !sameMessage(want[i], got[i]) {
			t.Fatalf("message %d differs between the log and the live history\nlog:  %s\nlive: %s",
				i, dump(got), dump(want))
		}
	}
	assertPaired(t, got)
}

func TestVerifyFeedbackReachesTheLog(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("1", "write_file", `{"path":"a.txt","content":"x\n"}`),
		text("all done"),
		text("really done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	mem := &session.Memory{}
	a.Log = mem
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"false"}}}
	if err := a.Run(context.Background(), "write it", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("made %d model calls, want 3: the failed check goes back to the model", len(f.requests))
	}
	assertLogFoldsToHistory(t, a, mem)
}

func TestStopHookFeedbackReachesTheLog(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("done"), text("still done"), text("done again"), text("really done")}}
	a, root := newAgent(t, f, nil)
	mem := &session.Memory{}
	a.Log = mem
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.Stop: {{Command: `echo "keep going" >&2; exit 2`}},
	}}}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) < 2 {
		t.Fatalf("the stop hook did not send the model back: %d calls", len(f.requests))
	}
	assertLogFoldsToHistory(t, a, mem)
}

func TestBudgetStopLeavesAPairedHistoryOnTheLog(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		costly(0.40, "1", "read_file", `{"path":"a.txt"}`),
		costly(0.40, "2", "read_file", `{"path":"a.txt"}`),
		costly(0.40, "3", "read_file", `{"path":"a.txt"}`),
	}}
	a, _ := newAgent(t, f, nil)
	a.MaxCostUSD = 1
	mem := &session.Memory{}
	a.Log = mem
	if err := a.Run(context.Background(), "read it a few times", nil); !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v, want ErrBudget", err)
	}
	assertLogFoldsToHistory(t, a, mem)
}

// An approver that cannot answer — a front-end that went away, a
// transport that broke — is neither the user cancelling nor the tool
// failing. It used to be swallowed: the batch answered every unfinished
// call with "Cancelled by user" and returned a nil error, so the turn
// carried on telling the model the user had refused.
type brokenApprover struct{ err error }

func (b brokenApprover) Approve(context.Context, ToolCall) (Decision, error) {
	return Deny, b.err
}

func TestApproverErrorStopsTheTurnInsteadOfLookingLikeACancel(t *testing.T) {
	boom := errors.New("the front-end went away")
	f := &fake{responses: []*llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockToolUse, ID: "1", Name: "read_file", Input: []byte(`{"path":"a.txt"}`)},
				{Type: llm.BlockToolUse, ID: "2", Name: "write_file", Input: []byte(`{"path":"b.txt","content":"x\n"}`)},
			}},
			StopReason: llm.StopToolUse,
			Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
		},
		text("done"),
	}}
	a, _ := newAgent(t, f, brokenApprover{err: boom})
	err := a.Run(context.Background(), "do it", nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the approver's error", err)
	}
	if len(f.requests) != 1 {
		t.Fatalf("made %d model calls, want 1: the turn must not continue past a broken approver", len(f.requests))
	}
	// The history still pairs, and the call that never ran is not
	// described as something the user did.
	assertPaired(t, a.Messages)
	last := a.Messages[len(a.Messages)-1]
	for _, b := range last.Blocks {
		if b.ToolUseID == "2" && !strings.Contains(b.Text, boom.Error()) {
			t.Fatalf("the unrun call was answered with %q, which does not say what went wrong", b.Text)
		}
	}
}
