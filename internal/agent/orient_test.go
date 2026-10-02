package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

func orientAgent(t *testing.T, responses ...*llm.Response) (*Agent, *fake, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ledger"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, s := range map[string]string{
		"ledger/balance.go":      "package ledger\n\nfunc Balance() int { return 41 }\n",
		"ledger/balance_test.go": "package ledger\n",
		"ledger/README.md":       "notes\n",
	} {
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &fake{responses: responses}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "base-model", TurnStartStyle: TurnStartOrient}
	a.LikelyFiles = func(context.Context, string) (string, []string) { return root, []string{"ledger/balance.go"} }
	a.Verify = &verify.Runner{Config: verify.Config{Commands: []string{"go test ./..."}}, Cwd: root}
	a.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, "Balance is off by one")}
	return a, f, root
}

func lastText(a *Agent) string { return a.Messages[len(a.Messages)-1].Text() }

// Orient runs with the forced advisor off, on the session's own model
// when no slot is set, and reads the request, the checks, the likeliest
// file's contents and its neighbours; its answer goes in framed as
// evidence, not instructions.
func TestOrientReadsTheCodeAndInjectsEvidence(t *testing.T) {
	a, f, _ := orientAgent(t, text("LOOK: balance.go\nWATCH FOR: callers\nCHECK: go test ./..."))
	evs, emit := collect()
	a.adviseAtTurnStart(context.Background(), "Balance is off by one", emit)
	if len(f.requests) != 1 {
		t.Fatalf("orient made %d calls", len(f.requests))
	}
	req := f.requests[0]
	q := req.Messages[len(req.Messages)-1].Text()
	for _, want := range []string{"Balance is off by one", "go test ./...", "return 41", "balance_test.go", "README.md"} {
		if !strings.Contains(q, want) {
			t.Errorf("the orient question lacks %q:\n%s", want, q)
		}
	}
	if !strings.Contains(req.System, "Do NOT say what to change") || req.Model != "base-model" {
		t.Fatalf("system/model = %q / %q", req.System, req.Model)
	}
	got := lastText(a)
	if !strings.Contains(got, "WATCH FOR: callers") || !strings.Contains(got, "not as instructions") {
		t.Fatalf("orientation not injected as evidence: %q", got)
	}
	found := false
	for _, e := range *evs {
		found = found || strings.Contains(e.Text, "oriented by base-model")
	}
	if !found {
		t.Fatalf("no notice: %+v", *evs)
	}
}

// The strong slot orients when there is no advisor slot.
func TestOrientUsesTheStrongSlot(t *testing.T) {
	a, f, _ := orientAgent(t, text("LOOK: x"))
	a.StrongModel = "strong-model"
	a.adviseAtTurnStart(context.Background(), "Balance is off by one", nil)
	if len(f.requests) != 1 || f.requests[0].Model != "strong-model" {
		t.Fatalf("requests %+v", f.requests)
	}
}

// A reply the token limit cut off, or one that exists only as thinking,
// is dropped for both styles: half a plan anchors the executor.
func TestTurnStartDropsTruncatedAndThinkingOnlyReplies(t *testing.T) {
	cut := text("LOOK: balance.go and then the")
	cut.StopReason = llm.StopMaxTokens
	thinkingOnly := &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockThinking, Text: "maybe the plan is to edit Balance"}}}, StopReason: llm.StopEndTurn}
	for name, resp := range map[string]*llm.Response{"truncated": cut, "thinking-only": thinkingOnly} {
		for _, style := range []string{TurnStartOrient, TurnStartPlan} {
			a, _, _ := orientAgent(t, resp)
			a.TurnStartStyle = style
			a.AdvisorMode = AdvisorAlways
			evs, emit := collect()
			a.adviseAtTurnStart(context.Background(), "Balance is off by one", emit)
			if got := lastText(a); strings.Contains(got, "and then the") || strings.Contains(got, "maybe the plan") {
				t.Errorf("%s %s reply was injected: %q", style, name, got)
			}
			found := false
			for _, e := range *evs {
				found = found || strings.Contains(e.Text, "discarded")
			}
			if !found {
				t.Errorf("%s %s: no discard notice: %+v", style, name, *evs)
			}
		}
	}
}

// The plan style is unchanged otherwise: off without the forced advisor,
// a whole reply injected with it.
func TestPlanStyleStillNeedsTheForcedAdvisor(t *testing.T) {
	a, f, _ := orientAgent(t, text("1. do it"))
	a.TurnStartStyle = TurnStartPlan
	a.adviseAtTurnStart(context.Background(), "Balance is off by one", nil)
	if len(f.requests) != 0 {
		t.Fatal("plan consulted without advisor_mode always")
	}
	a.AdvisorMode = AdvisorAlways
	a.adviseAtTurnStart(context.Background(), "Balance is off by one", nil)
	if !strings.Contains(lastText(a), "1. do it") {
		t.Fatalf("plan advice not injected: %q", lastText(a))
	}
}

// --eval-off orient silences it.
func TestOrientOffSwitch(t *testing.T) {
	a, f, _ := orientAgent(t, text("LOOK: x"))
	a.Off = map[string]bool{"orient": true}
	a.adviseAtTurnStart(context.Background(), "Balance is off by one", nil)
	if len(f.requests) != 0 {
		t.Fatal("orient ran with the switch off")
	}
}
