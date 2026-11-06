package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// A plan budget ends a reply that has reasoned past PlanBudget characters
// without writing a word or starting a tool call, and sends the model back
// to act (continueCutReply). With a whole small repository in its first
// message, a reasoning model tends to design the entire change before its
// first tool call: on effbench MiMo spent 23–33k reasoning tokens, six to
// eight minutes, on such a reply, and when one ran into the output limit
// the next call wrote the code after a few hundred tokens of thought. Told
// to act, MiMo sometimes planned for ten minutes again (d-minire), so the
// budget holds for maxPlanCuts replies a turn; after that a reply plans as
// long as the output limit lets it.

// The budget is also a time: Kimi streams reasoning at about 130
// characters a second, so 32k characters was four minutes of planning
// before the cut (realrepo hugo-joinpath: 236 s; effbench d-cronexpr:
// 248 s). A reply that has reasoned planBudgetTime without acting is cut
// too, once it has streamed planTimedMin characters, so a slow first
// token alone never ends a call.
const (
	planBudgetTime = 2 * time.Minute
	planTimedMin   = 4_000
)

// planKeepChars is how much of a cut reply's reasoning is kept as text.
// Thinking is not sent back on the OpenAI-compatible path, so without it
// the minutes of planning the cut ended were lost whole and the model
// planned again from nothing.
const planKeepChars = 3_000

// maxPlanCuts bounds how many replies of one turn the budget ends.
const maxPlanCuts = 3

// planBudgetNudge is what the model is told after a reply the budget ended.
const planBudgetNudge = "[harness] You have been reasoning for a long time without acting, so that reply was stopped. Stop planning: make your first change now with a tool call, writing the code in the call rather than in your reasoning, and work out the rest from what the change and the tests show."

// errPlanBudget is the cause a call is cancelled with when its reasoning
// ran past the plan budget.
var errPlanBudget = errors.New("reasoning ran past the plan budget")

// planWatch counts one call's reasoning until the reply acts.
type planWatch struct {
	budget  int
	chars   int
	acted   bool
	thought strings.Builder
	start   time.Time
	now     func() time.Time
}

// newPlanWatch returns the watch for the next call, or nil when the
// budget is off or this turn has used up its cuts.
func (a *Agent) newPlanWatch() *planWatch {
	if a.PlanBudget <= 0 || a.planCuts >= maxPlanCuts || a.Off["planbudget"] {
		return nil
	}
	return &planWatch{budget: a.PlanBudget, start: time.Now(), now: time.Now}
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
		if w.chars > w.budget {
			return true
		}
		return w.now != nil && w.chars >= planTimedMin && w.now().Sub(w.start) > planBudgetTime
	case llm.EventTextDelta:
		w.acted = w.acted || strings.TrimSpace(e.Text) != ""
	case llm.EventToolUseStart, llm.EventToolUseDelta:
		w.acted = true
	}
	return false
}

// cutReply is what a call ended by the budget leaves in the conversation:
// the reasoning so far, stopped at the limit, the same shape as a reply
// the provider cut at its output limit, and the reasoning's last part as
// text, which every provider sends back.
func (w *planWatch) cutReply(model string) *llm.Response {
	thought := w.thought.String()
	tail := thought
	if len(tail) > planKeepChars {
		tail = "…" + strings.ToValidUTF8(tail[len(tail)-planKeepChars:], "")
	}
	blocks := []llm.Block{{Type: llm.BlockThinking, Text: thought}}
	if strings.TrimSpace(tail) != "" {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: "(My reasoning was stopped here; where it had got to:)\n" + tail})
	}
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Blocks: blocks},
		StopReason: llm.StopMaxTokens,
		Model:      model,
	}
}

// planBudgetCut reports whether err is the plan budget ending a call
// rather than the user or the provider.
func planBudgetCut(ctx context.Context, err error) bool {
	return err != nil && errors.Is(context.Cause(ctx), errPlanBudget)
}
