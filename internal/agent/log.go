package agent

import "github.com/xmonader/janetcode/internal/session"

// SetPlanMode records plan/mode on the log so resume can fold it.
func (a *Agent) SetPlanMode(on bool) {
	a.PlanMode = on
	v := on
	a.logEvent(session.Event{Type: session.TypePlanMode, Active: &v})
}

func (a *Agent) logEvent(e session.Event) {
	if a.Log == nil {
		return
	}
	if e.Turn == 0 {
		e.Turn = a.turnN
	}
	if e.Step == 0 && a.stepN > 0 {
		e.Step = a.stepN
	}
	_ = a.Log.Append(e)
}
