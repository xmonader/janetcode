package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// guardedTool is a mutating tool that guards one of its inputs, the way
// bash guards a command reaching outside the workspace.
type guardedTool struct{ ran *[]string }

func (guardedTool) Name() string            { return "run" }
func (guardedTool) Description() string     { return "runs" }
func (guardedTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (guardedTool) Mutating() bool          { return true }
func (g guardedTool) Run(_ context.Context, input json.RawMessage) (string, error) {
	*g.ran = append(*g.ran, string(input))
	return "ok", nil
}

func (guardedTool) Guard(input json.RawMessage) (bool, string) {
	if strings.Contains(string(input), "outside") {
		return true, "writes outside the workspace"
	}
	return false, ""
}

// Auto-approve means "do not stop me for what would be allowed", never
// "skip the guard". The guard is what says a call reaches outside the
// workspace, and a session that answers "I could not tell, so fine" is
// not a guard — so a guarded call still needs an answer, and a run with
// nobody to answer (an eval, a headless run) is refused rather than let
// through. The model sees the refusal, which is the point: it fixes the
// command, not the machine.
func TestAutoApproveDoesNotSkipTheGuard(t *testing.T) {
	var ran []string
	f := &fake{responses: []*llm.Response{toolCall("c1", "run", `{"what":"outside"}`), text("done")}}
	a, _ := newAgent(t, f, nil)
	a.Tools = tool.NewRegistry(guardedTool{ran: &ran})
	a.AutoApprove = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("a guarded call ran under auto-approve with no approver: %v", ran)
	}
	if !strings.Contains(resultText(a), "Permission denied") {
		t.Fatalf("the model was not told why: %q", resultText(a))
	}
}

// The other half of the same rule: auto-approve still means no prompt
// for everything the guard has nothing to say about.
func TestAutoApproveStillRunsUnguardedCallsWithoutAsking(t *testing.T) {
	var ran []string
	f := &fake{responses: []*llm.Response{toolCall("c1", "run", `{"what":"inside"}`), text("done")}}
	a, _ := newAgent(t, f, nil)
	a.Tools = tool.NewRegistry(guardedTool{ran: &ran})
	a.AutoApprove = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 {
		t.Fatalf("auto-approve stopped for an unguarded call: %v / %q", ran, resultText(a))
	}
}

// With a user there to answer, Allow all is their choice to stop being
// asked: the guarded call runs without a prompt. The headless case above
// still refuses, since nobody chose anything there.
func TestAllowAllRunsAGuardedCallWithoutAsking(t *testing.T) {
	var ran []string
	asked := 0
	f := &fake{responses: []*llm.Response{toolCall("c1", "run", `{"what":"outside"}`), text("done")}}
	a, _ := newAgent(t, f, ApproverFunc(func(context.Context, ToolCall) (Decision, error) {
		asked++
		return Allow, nil
	}))
	a.Tools = tool.NewRegistry(guardedTool{ran: &ran})
	a.AutoApprove = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if asked != 0 || len(ran) != 1 {
		t.Fatalf("asked %d times, ran %v", asked, ran)
	}
}

func resultText(a *Agent) string {
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				return b.Text
			}
		}
	}
	return ""
}

// Away is Allow all with nobody watching: a guarded call is not waived
// and not put to the user, it is refused and listed for the return.
func TestAwayRefusesAGuardedCallAndListsIt(t *testing.T) {
	var ran []string
	asked := 0
	f := &fake{responses: []*llm.Response{toolCall("c1", "run", `{"what":"outside"}`), text("done")}}
	a, _ := newAgent(t, f, ApproverFunc(func(context.Context, ToolCall) (Decision, error) {
		asked++
		return Allow, nil
	}))
	a.Tools = tool.NewRegistry(guardedTool{ran: &ran})
	if _, err := a.SetAway(true); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if asked != 0 || len(ran) != 0 {
		t.Fatalf("asked %d times, ran %v", asked, ran)
	}
	if got := resultText(a); !strings.Contains(got, "the user is away") {
		t.Fatalf("result = %q", got)
	}
	notes, _ := a.SetAway(false)
	if got := FormatAwayNotes(notes); !strings.Contains(got, "1 call refused") || !strings.Contains(got, "run") {
		t.Fatalf("list = %q", got)
	}
}
