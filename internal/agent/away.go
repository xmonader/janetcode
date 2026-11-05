package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/managed"
)

// Away mode (/afk): the user stepped away. Approvals that would ask are
// answered as if they had chosen Allow all — it is the same
// Settings.AutoApprove, so deny rules, hard denials, egress and taint
// prompts behave exactly as under Allow all — and a question the model
// asks comes straight back with awayAnswer instead of waiting for nobody.
// The questions are kept for the return.

// awayAnswer is what the ask tool returns while the user is away.
const awayAnswer = "The user is away. State the assumption you are making, then continue; they will review it when they return."

// AwayNote is one question answered with an assumption while away.
type AwayNote struct {
	Question string
	Options  []string
	// Assumed is the option the model recommended, when it gave one; the
	// assumption it states in prose is in the transcript.
	Assumed string
}

type awayState struct {
	mu    sync.Mutex
	on    bool
	prev  [3]bool // AutoApprove, AutoMode, AutoEdits before /afk
	notes []AwayNote
}

// awayRef is the agent's away state, created on first use and shared by
// the subagents it delegates to.
func (a *Agent) awayRef() *awayState {
	if s := a.away.Load(); s != nil {
		return s
	}
	a.away.CompareAndSwap(nil, &awayState{})
	return a.away.Load()
}

// Away reports whether away mode is on.
func (a *Agent) Away() bool {
	s := a.away.Load()
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}

// SetAway turns away mode on or off. Turning it on is refused where a
// policy forbids Allow all; turning it off puts the permission mode back
// exactly as it was and returns the questions answered meanwhile.
func (a *Agent) SetAway(on bool) ([]AwayNote, error) {
	s := a.awayRef()
	s.mu.Lock()
	defer s.mu.Unlock()
	if on == s.on {
		return nil, nil
	}
	if on {
		if err := a.ModeRefusal(managed.ModeYolo); err != nil {
			return nil, err
		}
		a.UpdateSettings(func(st *Settings) {
			s.prev = [3]bool{st.AutoApprove, st.AutoMode, st.AutoEdits}
			st.AutoApprove, st.AutoMode, st.AutoEdits = true, false, false
		})
		s.on = true
		return nil, nil
	}
	a.UpdateSettings(func(st *Settings) { st.AutoApprove, st.AutoMode, st.AutoEdits = s.prev[0], s.prev[1], s.prev[2] })
	s.on = false
	notes := s.notes
	s.notes = nil
	return notes, nil
}

// TakeAwayNotes returns and clears the questions answered while away.
func (a *Agent) TakeAwayNotes() []AwayNote {
	s := a.away.Load()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	notes := s.notes
	s.notes = nil
	return notes
}

// AwayPending is how many answered questions wait for the user.
func (a *Agent) AwayPending() int {
	s := a.away.Load()
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.notes)
}

// recordAway keeps qs for the return.
func (a *Agent) recordAway(qs []Question) {
	s := a.awayRef()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, q := range qs {
		n := AwayNote{Question: q.Prompt, Assumed: q.Recommended}
		for _, o := range q.Options {
			n.Options = append(n.Options, o.Label)
		}
		s.notes = append(s.notes, n)
	}
}

// FormatAwayNotes is the list shown on return; empty for no notes.
func FormatAwayNotes(notes []AwayNote) string {
	if len(notes) == 0 {
		return ""
	}
	var sb strings.Builder
	noun := "questions"
	if len(notes) == 1 {
		noun = "question"
	}
	fmt.Fprintf(&sb, "While you were away: %d %s answered with assumptions", len(notes), noun)
	for _, n := range notes {
		sb.WriteString("\n  - " + n.Question)
		if n.Assumed != "" {
			sb.WriteString(" (recommended: " + n.Assumed + ")")
		}
	}
	return sb.String()
}
