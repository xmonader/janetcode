package agent

import (
	"errors"
	"fmt"
	"sync"

	"github.com/xmonader/janetcode/internal/session"
)

var ErrSessionLog = errors.New("session persistence failed")

// Keep the evidence of a failed append without replacing Log while
// concurrent tools and front-ends may still hold it. A failed session
// stops before further tool execution; SetLog installs a fresh sink.
type logState struct {
	mu     sync.Mutex
	err    error
	backup *session.Memory
	seq    int
}

func (a *Agent) persistenceErr() error {
	a.logState.mu.Lock()
	defer a.logState.mu.Unlock()
	return a.logState.err
}

func (a *Agent) logEvents() []session.Event {
	a.logState.mu.Lock()
	defer a.logState.mu.Unlock()
	if a.logState.backup != nil {
		return a.logState.backup.Events()
	}
	if a.Log == nil {
		return nil
	}
	return a.Log.Events()
}

// LogEvent records one event on the session log. It is how a tool that
// the agent owns — the archive reader, for one — puts what it did on the
// same trail as everything else, so a turn can be read back whole.
func (a *Agent) LogEvent(e session.Event) { a.logEvent(e) }

// SetPlanMode switches plan mode, safe while a turn runs, and records
// plan/mode on the log so resume can fold it.
func (a *Agent) SetPlanMode(on bool) {
	a.UpdateSettings(func(s *Settings) { s.PlanMode = on })
	v := on
	a.logEvent(session.Event{Type: session.TypePlanMode, Active: &v})
}

// logEvent appends one event to the session log and returns the seq it was
// given, or 0 when there is no log.
func (a *Agent) logEvent(e session.Event) int {
	a.logState.mu.Lock()
	defer a.logState.mu.Unlock()
	if a.Log == nil {
		return 0
	}
	if e.Turn == 0 {
		e.Turn = a.turnN
	}
	if e.Step == 0 && a.stepN > 0 {
		e.Step = a.stepN
	}
	if e.Type == session.TypeTurnEnd && e.Turn == a.turnN {
		a.turnEnded.Store(true)
	}
	if a.logState.backup != nil {
		a.logState.seq++
		e.Seq = a.logState.seq
		seq, _ := a.logState.backup.Append(e)
		return seq
	}
	seq, err := a.Log.Append(e)
	if err != nil {
		a.logState.err = fmt.Errorf("%w: %v; further tool execution is stopped", ErrSessionLog, err)
		a.logState.backup = &session.Memory{}
		for _, old := range a.Log.Events() {
			_, _ = a.logState.backup.Append(old)
			a.logState.seq = max(a.logState.seq, old.Seq)
		}
		// A nonzero seq means the sink kept the event despite a sync
		// or close error. A zero seq means it must be retained here.
		if seq == 0 {
			a.logState.seq++
			e.Seq = a.logState.seq
			seq, _ = a.logState.backup.Append(e)
		}
	}
	return seq
}

// SetLog points the agent at another session log — a fork, a cleared
// session, a resumed one — and re-seeds it from that log.
func (a *Agent) SetLog(l session.Log) {
	a.logState.mu.Lock()
	a.logState.err, a.logState.backup, a.logState.seq = nil, nil, 0
	a.Log = l
	a.logState.mu.Unlock()
	a.RestoreFromLog()
	a.usageMu.Lock()
	a.requestSessionID = ""
	a.usageMu.Unlock()
	a.budgetMu.Lock()
	if a.budget != nil && a.budget.owner == a {
		a.budget = nil // the next call seeds the new session's usage
	}
	a.budgetMu.Unlock()
}

// RestoreFromLog seeds the agent from its session log: the plan mode it was
// left in, and the turn counter, so new events do not reuse turn numbers
// that an earlier run already wrote.
func (a *Agent) RestoreFromLog() {
	if a.Log == nil {
		return
	}
	ev := a.logEvents()
	plan := session.FoldPlanMode(ev)
	a.UpdateSettings(func(s *Settings) { s.PlanMode = plan })
	a.restoreUsage(ev)
	a.turnN = 0
	for _, e := range ev {
		if e.Turn > a.turnN {
			a.turnN = e.Turn
		}
	}
}
