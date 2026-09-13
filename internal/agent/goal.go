package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
)

const (
	GoalActive        = "active"
	GoalPaused        = "paused"
	GoalBlocked       = "blocked"
	GoalComplete      = "complete"
	defaultGoalRounds = 8
)

// Goal is the fold of goal/* events for one id.
type Goal struct {
	ID        string
	Objective string
	Phase     string
	Reason    string
	MaxRounds int
	Rounds    int
}

// FoldGoals returns the latest snapshot per goal id.
func FoldGoals(events []session.Event) []Goal {
	by := map[string]*Goal{}
	var order []string
	for _, e := range events {
		switch e.Type {
		case session.TypeGoalCreate, session.TypeGoalPhase, session.TypeGoalRound:
		default:
			continue // tool calls and spills also carry a Name
		}
		id := e.Name
		if id == "" {
			continue
		}
		g := by[id]
		if g == nil {
			g = &Goal{ID: id, MaxRounds: defaultGoalRounds}
			by[id] = g
			order = append(order, id)
		}
		switch e.Type {
		case session.TypeGoalCreate:
			g.Objective = e.Output
			if e.Tokens > 0 {
				g.MaxRounds = e.Tokens
			}
			g.Phase = GoalActive
		case session.TypeGoalPhase:
			g.Phase = e.Reason
			g.Reason = e.Output
		case session.TypeGoalRound:
			g.Rounds++
		}
	}
	out := make([]Goal, 0, len(order))
	for _, id := range order {
		out = append(out, *by[id])
	}
	return out
}

// ActiveGoal is the last goal still in active phase, or nil.
func ActiveGoal(events []session.Event) *Goal { return LastGoal(events, GoalActive) }

// LastGoal is the most recent goal in the given phase, or the most recent
// goal of any phase when phase is "". It returns nil when there are none.
func LastGoal(events []session.Event, phase string) *Goal {
	gs := FoldGoals(events)
	for i := len(gs) - 1; i >= 0; i-- {
		if phase == "" || gs[i].Phase == phase {
			g := gs[i]
			return &g
		}
	}
	return nil
}

// TargetGoal picks which goal a bare /goal pause|resume|done acts on and
// the phase it moves to: resume targets the last paused goal, pause and
// done target the active one, and either falls back to the most recent
// goal of any phase. verb must be "pause", "resume" or "done".
func TargetGoal(events []session.Event, verb string) (*Goal, string, error) {
	g := ActiveGoal(events)
	if verb == "resume" {
		g = LastGoal(events, GoalPaused)
	}
	if g == nil {
		g = LastGoal(events, "")
	}
	if g == nil {
		return nil, "", fmt.Errorf("no goal")
	}
	phase := GoalPaused
	if verb == "resume" {
		phase = GoalActive
	}
	if verb == "done" {
		phase = GoalComplete
	}
	return g, phase, nil
}

// FormatGoals renders the /goal listing: one line per goal plus its
// reason, if any.
func FormatGoals(gs []Goal) string {
	var sb strings.Builder
	for _, g := range gs {
		fmt.Fprintf(&sb, "%s  %-8s  %d/%d  %s\n", g.ID, g.Phase, g.Rounds, g.MaxRounds, g.Objective)
		if g.Reason != "" {
			fmt.Fprintf(&sb, "    %s\n", g.Reason)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (a *Agent) CreateGoal(objective string, maxRounds int) string {
	if maxRounds <= 0 {
		maxRounds = defaultGoalRounds
	}
	// Numbered by how many goals the log already holds, so two goals made
	// inside one turn do not share an id (and fold into one).
	id := "g1"
	if a.Log != nil {
		id = fmt.Sprintf("g%d", len(FoldGoals(a.Log.Events()))+1)
	}
	a.logEvent(session.Event{Type: session.TypeGoalCreate, Name: id, Output: strings.TrimSpace(objective), Tokens: maxRounds})
	a.logEvent(session.Event{Type: session.TypeGoalPhase, Name: id, Reason: GoalActive})
	return id
}

func (a *Agent) SetGoalPhase(id, phase, reason string) {
	a.logEvent(session.Event{Type: session.TypeGoalPhase, Name: id, Reason: phase, Output: reason})
}

// GoalContinuation is the next auto-prompt while a goal is active, or "".
func (a *Agent) GoalContinuation() string {
	if a.Log == nil {
		return ""
	}
	g := ActiveGoal(a.Log.Events())
	if g == nil || g.Rounds >= g.MaxRounds {
		return ""
	}
	a.logEvent(session.Event{Type: session.TypeGoalRound, Name: g.ID})
	lead := "Continue the goal: "
	if g.Rounds == 0 {
		lead = "Work on this goal until it is complete or you are blocked: "
	}
	return lead + g.Objective + "\n(This is round " + fmt.Sprint(g.Rounds+1) + " of at most " + fmt.Sprint(g.MaxRounds) + ". Rounds are a budget, not a plan: finish in one if you can. Call goal_status complete only when the objective is fully met, or blocked with the reason; the user sees that call, so do not announce rounds in your reply.)"
}

// GoalStatus is the model-facing tool to finish or block the active goal.
type GoalStatus struct{ Parent *Agent }

func (*GoalStatus) Name() string { return "goal_status" }
func (*GoalStatus) Description() string {
	return "Mark the current session goal complete or blocked. Call this when the objective is done, or when you cannot proceed (include a short reason)."
}
func (*GoalStatus) Mutating() bool { return false }
func (*GoalStatus) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"status":{"type":"string","enum":["complete","blocked"],"description":"complete if the goal is done, blocked if you cannot proceed"},
"reason":{"type":"string","description":"short explanation, required when blocked"}},
"required":["status"],"additionalProperties":false}`)
}

func (g *GoalStatus) Run(_ context.Context, input json.RawMessage) (string, error) {
	if g.Parent == nil || g.Parent.Log == nil {
		return "", fmt.Errorf("no session log")
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	events := g.Parent.Log.Events()
	cur := ActiveGoal(events)
	if cur == nil {
		// A model that has just completed a goal often calls this again
		// on the next turn, to confirm. "Error: no active goal" reads as
		// something having gone wrong, so it tries once more, and again;
		// the settled goal is the answer it was looking for, and saying
		// so plainly is what stops the retry loop.
		if last := LastGoal(events, ""); last != nil {
			msg := "goal " + last.ID + " is already " + last.Phase
			if last.Reason != "" {
				msg += ": " + last.Reason
			}
			return msg + ". There is nothing left to mark; do not call goal_status again for it.", nil
		}
		return "", fmt.Errorf("no active goal")
	}
	phase := GoalComplete
	if in.Status == GoalBlocked || in.Status == "blocked" {
		phase = GoalBlocked
	}
	g.Parent.SetGoalPhase(cur.ID, phase, in.Reason)
	return "goal " + cur.ID + " is now " + phase, nil
}
