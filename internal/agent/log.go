package agent

import "github.com/xmonader/janetcode/internal/session"

// SetPlanMode records plan/mode on the log so resume can fold it.
func (a *Agent) SetPlanMode(on bool) {
	a.PlanMode = on
	v := on
	a.logEvent(session.Event{Type: session.TypePlanMode, Active: &v})
}

// logEvent appends one event to the session log and returns the seq it was
// given, or 0 when there is no log.
func (a *Agent) logEvent(e session.Event) int {
	if a.Log == nil {
		return 0
	}
	if e.Turn == 0 {
		e.Turn = a.turnN
	}
	if e.Step == 0 && a.stepN > 0 {
		e.Step = a.stepN
	}
	seq, err := a.Log.Append(e)
	if err != nil {
		return 0
	}
	return seq
}

// SetLog points the agent at another session log — a fork, a cleared
// session, a resumed one — and re-seeds it from that log.
func (a *Agent) SetLog(l session.Log) {
	a.Log = l
	a.RestoreFromLog()
}

// RestoreFromLog seeds the agent from its session log: the plan mode it was
// left in, and the turn counter, so new events do not reuse turn numbers
// that an earlier run already wrote.
func (a *Agent) RestoreFromLog() {
	if a.Log == nil {
		return
	}
	ev := a.Log.Events()
	a.PlanMode = session.FoldPlanMode(ev)
	a.turnN = 0
	for _, e := range ev {
		if e.Turn > a.turnN {
			a.turnN = e.Turn
		}
	}
}
