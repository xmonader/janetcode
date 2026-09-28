package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
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
			// A phase event may also carry a new round budget: what
			// /goal rounds sets, and the only way an exhausted goal can
			// be resumed at all (see SetGoalRounds).
			if e.Tokens > 0 {
				g.MaxRounds = e.Tokens
			}
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

// CreateGoal starts a goal and returns its id along with the ids of any
// goals it paused. A second active goal used to shadow the first: the
// model's goal_status acts on the newest active one, so on a recorded
// session a goal set by accident took the "complete" the model meant for
// the real one, and the real one then spent a whole round being told to
// continue work that was already done. Only one goal is active at a time
// now; a paused one comes back with /goal resume.
func (a *Agent) CreateGoal(objective string, maxRounds int) (string, []string) {
	if maxRounds <= 0 {
		maxRounds = defaultGoalRounds
	}
	var paused []string
	if a.Log != nil {
		for _, g := range FoldGoals(a.Log.Events()) {
			if g.Phase == GoalActive {
				a.SetGoalPhase(g.ID, GoalPaused, "paused by a new goal")
				paused = append(paused, g.ID)
			}
		}
	}
	// Numbered by how many goals the log already holds, so two goals made
	// inside one turn do not share an id (and fold into one).
	id := "g1"
	if a.Log != nil {
		id = fmt.Sprintf("g%d", len(FoldGoals(a.Log.Events()))+1)
	}
	a.logEvent(session.Event{Type: session.TypeGoalCreate, Name: id, Output: strings.TrimSpace(objective), Tokens: maxRounds})
	a.logEvent(session.Event{Type: session.TypeGoalPhase, Name: id, Reason: GoalActive})
	return id, paused
}

// goalRoundsRe matches the "rounds=20" or "rounds 20" a /goal argument
// may begin with. A bare leading number is deliberately not accepted:
// "/goal 3 bugs need fixing" is an objective, not a budget of three.
var goalRoundsRe = regexp.MustCompile(`^rounds[ =]+([0-9]{1,4})\s*`)

// maxGoalRounds bounds what a goal's budget can be set to. The budget is
// what stops a loop that is going nowhere from running all night.
const maxGoalRounds = 100

// ParseGoalRounds reads a leading round budget off a /goal argument and
// returns it with what is left — the objective, or "" when the argument
// only changes the budget. ok is false when there is no budget in it.
func ParseGoalRounds(arg string) (rounds int, rest string, ok bool) {
	m := goalRoundsRe.FindStringSubmatch(strings.TrimSpace(arg))
	if m == nil {
		return 0, arg, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0, arg, false
	}
	if n > maxGoalRounds {
		n = maxGoalRounds
	}
	return n, strings.TrimSpace(strings.TrimSpace(arg)[len(m[0]):]), true
}

// SetGoalRounds gives a goal a new round budget and makes it active
// again. It is how a goal that used up its rounds is carried on with:
// resuming one on its own could not work, because the loop refuses a
// round the moment the budget is spent, so /goal resume on a spent goal
// did nothing at all and said nothing either.
func (a *Agent) SetGoalRounds(id string, rounds int) {
	if rounds <= 0 {
		return
	}
	if rounds > maxGoalRounds {
		rounds = maxGoalRounds
	}
	a.logEvent(session.Event{Type: session.TypeGoalPhase, Name: id, Reason: GoalActive, Tokens: rounds})
}

// SpentGoal is the active goal that has used up its round budget, paused
// so that it says so. Without this the loop simply stopped: no round was
// sent, nothing was said, and the goal stayed "active" in the status line
// and in /goal for the rest of the session.
func (a *Agent) SpentGoal() (Goal, bool) {
	if a.Log == nil {
		return Goal{}, false
	}
	g := ActiveGoal(a.Log.Events())
	if g == nil || g.Rounds < g.MaxRounds {
		return Goal{}, false
	}
	reason := fmt.Sprintf("its %d rounds are used up; /goal rounds %d gives it more", g.MaxRounds, g.MaxRounds*2)
	a.SetGoalPhase(g.ID, GoalPaused, reason)
	return *g, true
}

// SpentNote is the one line a UI shows for a goal that ran out of rounds.
func SpentNote(g Goal) string {
	return fmt.Sprintf("goal %s stopped: %d of %d rounds used and it is not complete · /goal rounds %d continues it · /goal done closes it", g.ID, g.Rounds, g.MaxRounds, g.MaxRounds*2)
}

// GoalReopened prefixes the reason on a goal the harness put back to
// active: the round that called it complete ended on a red check.
const GoalReopened = "reopened: "

// goalCompletedThisTurn is the goal, if any, that was marked complete
// during the turn now ending.
func (a *Agent) goalCompletedThisTurn() *Goal {
	if a.Log == nil {
		return nil
	}
	ev := a.Log.Events()
	id := ""
	for _, e := range ev {
		if e.Type == session.TypeGoalPhase && e.Turn == a.turnN && e.Reason == GoalComplete {
			id = e.Name
		}
	}
	if id == "" {
		return nil
	}
	for _, g := range FoldGoals(ev) {
		if g.ID == id && g.Phase == GoalComplete {
			return &g
		}
	}
	return nil
}

// goalWasReopened reports whether this goal has been put back once
// already. The log is what remembers it: the goal's current reason is
// whatever the last phase change wrote, and completing it again clears
// it.
func (a *Agent) goalWasReopened(id string) bool {
	if a.Log == nil {
		return false
	}
	for _, e := range a.Log.Events() {
		if e.Type == session.TypeGoalPhase && e.Name == id && strings.HasPrefix(e.Output, GoalReopened) {
			return true
		}
	}
	return false
}

// ReopenCompletedGoal puts back a goal the model called complete in a
// turn whose own checks came back red. The two never spoke: goal_status
// is a tool call made partway through the turn and the verdict is folded
// at the end of it, so a session closed its goal on a suite that was
// failing and stopped, with the red check reported beside the completion
// and nothing drawing the conclusion.
//
// Once per goal. The model may have a reason the harness cannot see —
// the red check is someone else's, the failure is the point — and a
// harness that reopens a goal every time it is closed would argue until
// the round budget ran out.
func (a *Agent) ReopenCompletedGoal(outcome TurnOutcome, emit func(Event)) {
	if a.Off["verdict"] || outcome.Verdict != VerdictCheckFailed {
		return
	}
	g := a.goalCompletedThisTurn()
	if g == nil || a.goalWasReopened(g.ID) {
		return
	}
	why := "the round that called it complete ended red — " + outcome.Why
	a.SetGoalPhase(g.ID, GoalActive, GoalReopened+why)
	if emit != nil {
		emit(Event{Kind: EvNotice, Text: "goal " + g.ID + " reopened: " + why})
	}
}

// PausedNote is the one line a UI shows when starting a goal paused
// others, so a goal that stops making rounds says why rather than going
// quiet.
func PausedNote(paused []string) string {
	return strings.Join(paused, ", ") + " paused; /goal resume brings the last one back"
}

func (a *Agent) SetGoalPhase(id, phase, reason string) {
	a.logEvent(session.Event{Type: session.TypeGoalPhase, Name: id, Reason: phase, Output: reason})
}

// GoalContinuation is the next auto-prompt while a goal is active, or "".
// ctx bounds the optional planner/skeptic side query; the prompt itself
// is still built if that query fails or is skipped.
func (a *Agent) GoalContinuation(ctx context.Context) string {
	g, ok := a.nextGoalRound()
	if !ok {
		return ""
	}
	body := goalPrompt(g)
	if note := a.goalRoleNote(ctx, g); note != "" {
		body += "\n\n" + note
	}
	return body
}

func (a *Agent) nextGoalRound() (Goal, bool) {
	if a.Log == nil {
		return Goal{}, false
	}
	g := ActiveGoal(a.Log.Events())
	if g == nil || g.Rounds >= g.MaxRounds {
		return Goal{}, false
	}
	a.logEvent(session.Event{Type: session.TypeGoalRound, Name: g.ID})
	return *g, true
}

func goalPrompt(g Goal) string {
	lead := "Continue the goal: "
	if g.Rounds == 0 {
		lead = "Work on this goal until it is complete or you are blocked: "
	}
	body := lead + g.Objective + "\n(This is round " + fmt.Sprint(g.Rounds+1) + " of at most " + fmt.Sprint(g.MaxRounds) + ". Rounds are a budget, not a plan: finish in one if you can. Call goal_status complete only when the objective is fully met, or blocked with the reason; the user sees that call, so do not announce rounds in your reply.)"
	if reason, ok := strings.CutPrefix(g.Reason, GoalReopened); ok {
		body += "\nYou called this goal complete last round and the harness has put it back: " + reason +
			". Make the check pass, or call goal_status blocked saying why the red check is not yours to fix. Calling it complete again without either will not be questioned a second time."
	}
	return body
}

// GoalStatus is the model-facing tool to finish or block the active goal.
type GoalStatus struct{ Parent *Agent }

func (*GoalStatus) Name() string { return "goal_status" }
func (*GoalStatus) Description() string {
	return "Mark the goal the user set with /goal complete, or blocked with a short reason. Not for ordinary requests: just answer those."
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
		// No goal was ever set: the model finished an ordinary request
		// and reached for this to sign off. An error here showed as a
		// failed step and set it answering the whole request again.
		return "No goal is set in this session, so there is nothing to mark. Finish with your answer; do not call goal_status.", nil
	}
	// Anything but the two statuses the schema names is refused: it used
	// to count as complete, so a model reporting "in_progress" or
	// "failed" closed the goal it meant to keep working on.
	var phase string
	switch strings.ToLower(strings.TrimSpace(in.Status)) {
	case GoalComplete:
		phase = GoalComplete
	case GoalBlocked:
		phase = GoalBlocked
	default:
		return "", fmt.Errorf("status must be complete or blocked, not %q; if the goal is not done, keep working and do not call goal_status", in.Status)
	}
	g.Parent.SetGoalPhase(cur.ID, phase, in.Reason)
	return "goal " + cur.ID + " is now " + phase, nil
}
