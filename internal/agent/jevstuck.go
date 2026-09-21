package agent

import (
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The mid-turn "stuck" consumer, offline only. Nothing in a live turn
// calls any of this: it exists so --decisions-replay can score, against
// a corpus labelled by construction from recorded sessions, whether Jev
// can spot a wasted step while the turn is still running. Wiring it
// into the loop is a separate decision that needs these numbers first.
const (
	qStuck            = "stuck"
	qOnGoal           = "on_goal"
	qNextCallWillFail = "next_call_will_fail"
)

// Budgets for StuckState. A mid-turn consult must not carry the
// conversation: it carries the goal and a bounded window of recent tool
// activity, the same discipline triageState uses.
const (
	stuckGoalBudget  = 1000
	stuckLineBudget  = 220
	stuckStateBudget = 2500
	// StuckWindow is how many recent tool calls the state describes.
	StuckWindow = 8
)

// StuckStep is one recorded tool call as the state describes it: what
// was called, the arguments that identify it, and how it came back.
type StuckStep struct {
	Tool    string
	Args    string
	Failed  bool
	Outcome string
}

// StuckState renders the goal plus the last StuckWindow tool calls. It
// is deliberately the same shape a live consumer could build from the
// session log without re-reading any file.
func StuckState(goal string, steps []StuckStep) string {
	var b strings.Builder
	b.WriteString("Goal: " + textfmt.Clip(goal, stuckGoalBudget) + "\n")
	if len(steps) > StuckWindow {
		steps = steps[len(steps)-StuckWindow:]
	}
	if len(steps) == 0 {
		b.WriteString("Recent tool calls: none yet\n")
		return textfmt.Clip(b.String(), stuckStateBudget)
	}
	b.WriteString("Recent tool calls, oldest first:\n")
	for i, s := range steps {
		line := strconv.Itoa(i+1) + ". " + s.Tool + " " + s.Args + " -> "
		if s.Failed {
			line += "ERROR: "
		}
		line += s.Outcome
		b.WriteString(textfmt.Clip(line, stuckLineBudget) + "\n")
	}
	return textfmt.Clip(b.String(), stuckStateBudget)
}

// StuckQuestions is the candidate mid-turn question set.
func StuckQuestions() []jev.Question {
	return []jev.Question{
		{ID: qStuck, Kind: jev.Noul,
			Instructions: "Is this agent repeating itself without making progress?",
			NoulCriteria: map[string]string{
				"true":  "the recent calls repeat the same tool on the same target, or retry after the same error, without the result changing",
				"false": "each recent call does something new, or the results are changing in a way that moves the work along",
			}},
		{ID: qOnGoal, Kind: jev.Noul,
			Instructions: "Do the recent tool calls serve the stated goal?",
			NoulCriteria: map[string]string{
				"true":  "the files and commands touched are ones the goal is about",
				"false": "the calls have wandered onto something the goal did not ask for",
			}},
		{ID: qNextCallWillFail, Kind: jev.Noul,
			Instructions: "Is the next tool call this agent makes likely to fail the way the recent ones did?",
			NoulCriteria: map[string]string{
				"true":  "the recent errors have a cause the agent has not addressed, so another attempt of the same kind fails again",
				"false": "the last error was understood and acted on, or there were no errors",
			}},
	}
}

// StuckThreshold is the P(true) at which the replay scorer calls a step
// wasted. It is a starting point to sweep, not a tuned constant.
const StuckThreshold = 0.5

// StuckAction maps the answers to the one call a mid-turn consumer
// would make: interrupt this step, or leave it alone. Kept pure so it
// is testable without a provider, like triageAction.
func StuckAction(res *jev.Result, threshold float64) (flag bool, stuck, onGoal, nextFail float64) {
	if res == nil {
		return false, 0, 0, 0
	}
	stuck, _ = res.NoulAt(qStuck)
	onGoal, _ = res.NoulAt(qOnGoal)
	nextFail, _ = res.NoulAt(qNextCallWillFail)
	return stuck >= threshold, stuck, onGoal, nextFail
}
