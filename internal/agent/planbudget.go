package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// A plan budget ends a reply that has reasoned past PlanBudget characters
// without writing a word or starting a tool call, and sends the model back
// to act (continueCutReply). With a whole small repository in its first
// message, a reasoning model tends to design the entire change before its
// first tool call: on effbench MiMo spent 23–33k reasoning tokens, six to
// eight minutes, on such a reply, and when one ran into the output limit
// the next call wrote the code after a few hundred tokens of thought. The
// budget applies once a turn: a model that plans long again after being
// told to act gets to finish.

// errPlanBudget is the cause a call is cancelled with when its reasoning
// ran past the plan budget.
var errPlanBudget = errors.New("reasoning ran past the plan budget")

// planWatch counts one call's reasoning until the reply acts.
type planWatch struct {
	budget  int
	chars   int
	acted   bool
	thought strings.Builder
}

// newPlanWatch returns the watch for the next call, or nil when the
// budget is off or this turn has already used it.
func (a *Agent) newPlanWatch() *planWatch {
	if a.PlanBudget <= 0 || a.planCut || a.Off["planbudget"] {
		return nil
	}
	return &planWatch{budget: a.PlanBudget}
}

// note takes one stream event and reports whether the call should end.
func (w *planWatch) note(e llm.Event) bool {
	if w == nil || w.acted {
		return false
	}
	switch e.Type {
	case llm.EventThinkingDelta:
		w.chars += len(e.Text)
		w.thought.WriteString(e.Text)
		return w.chars > w.budget
	case llm.EventTextDelta:
		w.acted = w.acted || strings.TrimSpace(e.Text) != ""
	case llm.EventToolUseStart, llm.EventToolUseDelta:
		w.acted = true
	}
	return false
}

// cutReply is what a call ended by the budget leaves in the conversation:
// the reasoning so far, stopped at the limit, the same shape as a reply
// the provider cut at its output limit.
func (w *planWatch) cutReply(model string) *llm.Response {
	return &llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: w.thought.String()},
		}},
		StopReason: llm.StopMaxTokens,
		Model:      model,
	}
}

// planBudgetCut reports whether err is the plan budget ending a call
// rather than the user or the provider.
func planBudgetCut(ctx context.Context, err error) bool {
	return err != nil && errors.Is(context.Cause(ctx), errPlanBudget)
}
