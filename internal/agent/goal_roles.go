package agent

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	goalPlannerSystem = `You are planning one round of a coding-agent goal. No tools. Reply with: 1) the one next concrete step, 2) which files to look at, 3) how to know the step worked. Under 200 words.`
	goalSkepticSystem = `You are a skeptic on a coding-agent goal. The last round may have claimed progress. No tools. Reply with: 1) what is still unproven, 2) the cheapest check, 3) whether to continue or stop. Under 200 words.`
)

const goalRoleTimeout = 15 * time.Second

// goalRoleNote is optional orientation for the next goal round. It uses
// the same side-query path as advisor (AdvisorModel when set). It never
// replaces GoalContinuation; it prepends a short note or stays silent.
func (a *Agent) goalRoleNote(ctx context.Context, g Goal) string {
	if a == nil || g.Objective == "" || a.AdvisorModelName() == "" {
		return ""
	}
	if ctx == nil || ctx.Err() != nil {
		// No invented Background(): no caller context means no I/O.
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, goalRoleTimeout)
	defer cancel()
	model := a.AdvisorModelName()
	var (
		sys, q, tag string
	)
	if g.Rounds <= 1 {
		sys, tag = goalPlannerSystem, "planner"
		q = "Goal: " + g.Objective + "\nThis is the first working round. What is the one next step?"
	} else {
		sys, tag = goalSkepticSystem, "skeptic"
		q = "Goal: " + g.Objective + fmt.Sprintf("\nRound %d of %d just finished.", g.Rounds, g.MaxRounds)
		if o := a.Outcome(); o.Why != "" {
			q += "\nLast verdict: " + o.Line()
		}
		q += "\nWhat is still unproven?"
	}
	text, _, err := a.sideQuery(ctx, sys, q, model, 400)
	if err != nil || strings.TrimSpace(text) == "" {
		return ""
	}
	return "[" + tag + "]\n" + strings.TrimSpace(text)
}
