package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestRepeatedTruncatedToolCallsStopBeforeIterationLimit(t *testing.T) {
	f := &fake{}
	for i := range 10 {
		r := toolCall(fmt.Sprintf("w%d", i), "write_file", fmt.Sprintf(`{"path":"a.txt","content":"partial %d"}`, i))
		r.StopReason = llm.StopMaxTokens
		f.responses = append(f.responses, r)
	}
	a, root := newAgent(t, f, nil)
	a.AutoApprove, a.MaxIterations, a.MaxTokens = true, 10, 100
	evs, emit := collect()
	if err := a.Run(context.Background(), "write", emit); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("err=%v, want ErrOutputLimit", err)
	}
	if len(f.requests) != maxCutReplies+1 {
		t.Fatalf("requests=%d, want %d", len(f.requests), maxCutReplies+1)
	}
	if got, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(got) != "alpha\n" {
		t.Fatalf("truncated call changed file: %q, err=%v", got, err)
	}
	for _, e := range *evs {
		if e.Kind == EvToolRun || e.Kind == EvTurnDone {
			t.Fatalf("truncated calls ran or finished successfully: %+v", e)
		}
	}
	assertPaired(t, a.MessagesSnapshot())
	// The retry allowance belongs to a turn; a later complete call is
	// still runnable and no unanswered tool use poisons its request.
	a.Provider = &fake{responses: []*llm.Response{
		toolCall("complete", "write_file", `{"path":"a.txt","content":"complete"}`), text("done"),
	}}
	if err := a.Run(context.Background(), "try a smaller edit", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "complete" {
		t.Fatalf("next turn could not recover: %q", got)
	}
}

func TestTextCutsDoNotSpendTruncatedToolRetryAllowance(t *testing.T) {
	f := &fake{}
	for i := range maxCutReplies {
		r := text(fmt.Sprintf("partial explanation %d", i))
		r.StopReason = llm.StopMaxTokens
		f.responses = append(f.responses, r)
	}
	for i := range maxCutReplies + 1 {
		r := toolCall(fmt.Sprintf("w%d", i), "write_file", fmt.Sprintf(`{"path":"a.txt","content":"partial %d"}`, i))
		r.StopReason = llm.StopMaxTokens
		f.responses = append(f.responses, r)
	}
	a, _ := newAgent(t, f, nil)
	a.MaxIterations = 10
	if err := a.Run(context.Background(), "go", nil); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("err=%v, want ErrOutputLimit", err)
	}
	if len(f.requests) != 2*maxCutReplies+1 {
		t.Fatalf("text cuts reduced tool retries: requests=%d, want %d", len(f.requests), 2*maxCutReplies+1)
	}
	assertPaired(t, a.MessagesSnapshot())
}
