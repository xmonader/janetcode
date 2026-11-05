package agent

import (
	"fmt"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/managed"
)

// Away mode (/afk): the user stepped away. Approvals that would ask are
// answered as under Allow all (the same Settings.AutoApprove), except
// that nobody is there to see a warning: the local guards are not waived
// (yoloChosen), and any call that would still ask — a guard, a protected
// path, egress, taint, a hook's or a rule's ask — is refused and kept for
// the return (awayRefusal). A question the model asks comes straight back
// with awayAnswer instead of waiting for nobody.
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
	// Refused is set for a call that would have asked: away, it was
	// refused instead, and Question describes the call.
	Refused bool
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

// awayRefusal refuses, while away, a call that would put itself to the
// user (a guard, a protected path, egress, taint, a hook's or a rule's
// ask) and keeps it for the return. Empty when not away.
func (a *Agent) awayRefusal(name, subject, warning string) string {
	if !a.Away() {
		return ""
	}
	what := name
	if subject != "" {
		what += " " + subject
	}
	if warning != "" {
		what += " (" + warning + ")"
	}
	s := a.awayRef()
	s.mu.Lock()
	s.notes = append(s.notes, AwayNote{Question: what, Refused: true})
	s.mu.Unlock()
	return "Permission denied: the user is away and this call needs their approval. Continue with work that does not need it; they will see it when they return."
}

// FormatAwayNotes is the list shown on return; empty for no notes.
func FormatAwayNotes(notes []AwayNote) string {
	var asked, refused []AwayNote
	for _, n := range notes {
		if n.Refused {
			refused = append(refused, n)
		} else {
			asked = append(asked, n)
		}
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	}
	var sb strings.Builder
	if len(asked) > 0 {
		fmt.Fprintf(&sb, "While you were away: %d %s answered with assumptions", len(asked), plural(len(asked), "question", "questions"))
		for _, n := range asked {
			sb.WriteString("\n  - " + n.Question)
			if n.Assumed != "" {
				sb.WriteString(" (recommended: " + n.Assumed + ")")
			}
		}
	}
	if len(refused) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "While you were away: %d %s refused because %s your approval", len(refused), plural(len(refused), "call", "calls"), plural(len(refused), "it needs", "they need"))
		for _, n := range refused {
			sb.WriteString("\n  - " + n.Question)
		}
	}
	return sb.String()
}
