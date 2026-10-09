package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// thinker streams each scripted reply's reasoning in 100-character chunks
// before its text and tool calls, and stops when the call is cancelled,
// as a provider does.
type thinker struct {
	fake
	reasoning []int // characters of reasoning per call
	streamed  []int // characters each call got out before it ended
}

func (f *thinker) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	i := len(f.requests)
	n := 0
	if i < len(f.reasoning) {
		n = f.reasoning[i]
	}
	sent := 0
	defer func() { f.streamed = append(f.streamed, sent) }()
	for sent < n {
		emit(llm.Event{Type: llm.EventThinkingDelta, Text: strings.Repeat("x", 100)})
		sent += 100
		if err := ctx.Err(); err != nil {
			f.requests = append(f.requests, req)
			return nil, err
		}
	}
	return f.fake.Stream(ctx, req, emit)
}

// A reply that reasons past the budget without acting is ended and the
// model is told to act; the output limit is not raised for it, and the
// budget is spent for the turn, so a second long plan runs to the end.
func TestPlanBudgetEndsALongPlanOnce(t *testing.T) {
	f := &thinker{
		fake:      fake{responses: []*llm.Response{nil, text("planned again"), nil, text("next turn")}},
		reasoning: []int{50_000, 50_000, 50_000},
	}
	a, _ := newAgent(t, &f.fake, nil)
	a.Provider = f
	a.PlanBudget = 1_000
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(f.requests))
	}
	if f.streamed[0] > 1_100 || f.streamed[1] != 50_000 {
		t.Fatalf("reasoning streamed per call = %v, want the first ended near 1000 and the second whole", f.streamed)
	}
	if got := diagLastText(f.requests[1]); !strings.Contains(got, "take the next step now") {
		t.Fatalf("second request ends with %q", got)
	}
	if f.requests[1].MaxTokens != f.requests[0].MaxTokens {
		t.Fatalf("output limit went %d -> %d; the budget is not the output limit", f.requests[0].MaxTokens, f.requests[1].MaxTokens)
	}
	// The next turn has a budget of its own.
	if err := a.Run(context.Background(), "more", nil); err != nil {
		t.Fatal(err)
	}
	if f.streamed[2] > 1_100 {
		t.Fatalf("the next turn's first call streamed %d characters of reasoning, want it ended at the budget", f.streamed[2])
	}
}

// Reasoning that comes after the reply has started a tool call, or a
// budget that is off, never ends a call.
func TestPlanBudgetLeavesActingRepliesAlone(t *testing.T) {
	f := &thinker{
		fake:      fake{responses: []*llm.Response{text("done")}},
		reasoning: []int{50_000},
	}
	a, _ := newAgent(t, &f.fake, nil)
	a.Provider = f
	a.PlanBudget = 1_000
	a.Off = map[string]bool{"planbudget": true}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 || f.streamed[0] != 50_000 {
		t.Fatalf("calls = %d, streamed = %v; the switch is off", len(f.requests), f.streamed)
	}

	w := &planWatch{budget: 10}
	w.note(llm.Event{Type: llm.EventToolUseStart, Block: &llm.Block{Name: "bash"}})
	if w.note(llm.Event{Type: llm.EventThinkingDelta, Text: strings.Repeat("x", 100)}) {
		t.Fatal("reasoning after a tool call started ended the call")
	}
}
