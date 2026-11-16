package evidence

import (
	"fmt"
	"strings"
	"time"
)

// Advisory is a classification from the optional decision service about
// a failed check. It is recorded next to the check and never folded into
// its Status: the service can be wrong, slow or down, and the command's
// result must read the same either way.
type Advisory struct {
	// Source names the classifier ("jev").
	Source string `json:"source"`
	// Consumer is the question set that asked ("verify_triage").
	Consumer string `json:"consumer,omitempty"`
	// Class is the answer: patch_caused, environmental, flaky, unrelated
	// or inconclusive. Empty when the call did not answer.
	Class      string  `json:"class,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	// CausedBy is the probability the change caused the failure.
	CausedBy float64 `json:"caused_by_change,omitempty"`
	// Acted is true when the advice changed what the harness did (a
	// repair round was skipped).
	Acted bool `json:"acted_on"`
	// Error is why there is no class: "error", "timeout" or
	// "circuit_open". The check's own status is unaffected.
	Error string `json:"error,omitempty"`
}

// Check is one run of one verification command, as recorded.
type Check struct {
	Command string `json:"command"`
	// Source is what ran it: "verify" and "accept" are the turn-end
	// gates, "bash" a check the model ran itself.
	Source string `json:"source"`
	Turn   int    `json:"turn,omitempty"`
	Status Status `json:"status"`
	Exit   int    `json:"exit"`
	// Started and Ended bracket the run. Ended is when it was recorded;
	// Started is that minus the measured duration.
	Started time.Time `json:"started,omitempty"`
	Ended   time.Time `json:"ended,omitempty"`
	// Summary is the one line of output worth reading.
	Summary string `json:"summary,omitempty"`
	// Files are the files edited before the run: what it was run against.
	Files []string `json:"files,omitempty"`
	// Fresh is true when nothing that matters was edited after the run.
	Fresh bool `json:"fresh"`
	// StaleBy is the first file edited after the run, when it is not
	// Fresh.
	StaleBy string `json:"stale_by,omitempty"`
	// Attempts counts the runs of this command on one unchanged tree;
	// more than one with different outcomes makes the status Flaky.
	Attempts int `json:"attempts,omitempty"`
	// Withheld marks a held-out check whose command is a label, not the
	// script that ran.
	Withheld bool `json:"withheld,omitempty"`
	// Advisory is the decision service's opinion on a failure, if asked.
	Advisory *Advisory `json:"advisory,omitempty"`

	// idx is the position of the run in the log, for ordering.
	idx int
}

// Report is what a set of checks add up to against the edits made.
type Report struct {
	// Status is the final status: the worst status among the fresh
	// checks, Passed only when every fresh check passed, and NotRun when
	// nothing fresh ran.
	Status Status `json:"status"`
	// Required says the work edited code, so a success claim needs
	// evidence.
	Required bool `json:"required"`
	// Blocks is true when a success claim is not backed: evidence was
	// required and the final status is not Passed.
	Blocks bool `json:"blocks"`
	// Stale is true when every check that ran predates the last edit.
	Stale bool `json:"stale,omitempty"`
	// Edited is how many files were edited that need evidence.
	Edited int `json:"edited"`
	// Why is the reason for the final status in one line.
	Why    string  `json:"why"`
	Checks []Check `json:"checks,omitempty"`
}

// assess fills the verdict fields of r from its Checks, Required and
// Edited. Only fresh checks speak for the tree as it now stands.
func (r *Report) assess() {
	var fresh, stale []Check
	for _, c := range r.Checks {
		if c.Fresh {
			fresh = append(fresh, c)
		} else {
			stale = append(stale, c)
		}
	}
	r.Stale = len(fresh) == 0 && len(stale) > 0
	r.Status = Passed
	switch {
	case len(fresh) == 0:
		r.Status = NotRun
		switch {
		case len(stale) > 0:
			r.Why = fmt.Sprintf("every check predates the last edit (%s)", firstNonEmpty(stale[len(stale)-1].StaleBy, "a changed file"))
		case r.Required:
			r.Why = fmt.Sprintf("%s edited and no check ran", plural(r.Edited, "file"))
		default:
			r.Why = "no check ran"
		}
	default:
		have := map[Status][]Check{}
		for _, c := range fresh {
			have[c.Status] = append(have[c.Status], c)
		}
		for _, s := range All {
			if cs := have[s]; len(cs) > 0 {
				r.Status = s
				break
			}
		}
		if r.Status == Passed {
			r.Why = fmt.Sprintf("%s passed after the last edit", plural(len(fresh), "check"))
		} else {
			c := have[r.Status][0]
			r.Why = fmt.Sprintf("%s: %s (%s)", labelOf(c.Command), r.Status, c.Summary)
		}
	}
	r.Blocks = r.Required && r.Status != Passed
}

// Without returns r with the named commands dropped and the final status
// recomputed: a check the caller has retired (a red scratch script the
// model replaced with the real command) no longer counts.
func (r Report) Without(commands map[string]bool) Report {
	if len(commands) == 0 {
		return r
	}
	out := r
	out.Checks = nil
	for _, c := range r.Checks {
		if !commands[c.Command] {
			out.Checks = append(out.Checks, c)
		}
	}
	out.assess()
	return out
}

// Count returns how many checks have each status.
func (r Report) Count() map[Status]int {
	m := map[Status]int{}
	for _, c := range r.Checks {
		m[c.Status]++
	}
	return m
}

// Line is the one-line form for a turn verdict and for /status.
func (r Report) Line() string {
	if len(r.Checks) == 0 && !r.Required {
		return ""
	}
	s := fmt.Sprintf("%s evidence: %s", r.Status.Mark(), r.Status)
	if r.Stale {
		s += " (stale)"
	}
	if r.Blocks {
		s += ": " + r.Why + " · a success claim is not backed"
	}
	return s
}

// Text renders the whole report for a person: the final status and one
// line per check, with its freshness and any advisory.
func (r Report) Text() string {
	var b strings.Builder
	head := fmt.Sprintf("evidence: %s %s", r.Status.Mark(), r.Status)
	if r.Blocks {
		head += " · success claims blocked"
	}
	fmt.Fprintf(&b, "%s\n  %s\n", head, r.Why)
	for _, c := range r.Checks {
		fresh := "fresh"
		if !c.Fresh {
			fresh = "stale (edited after: " + firstNonEmpty(c.StaleBy, "a file") + ")"
		}
		fmt.Fprintf(&b, "  %s %s · %s · exit %d · %s · %s", c.Status.Mark(), labelOf(c.Command), c.Status, c.Exit, duration(c), fresh)
		if c.Attempts > 1 {
			fmt.Fprintf(&b, " · %d attempts", c.Attempts)
		}
		if c.Source != "" {
			b.WriteString(" · " + c.Source)
		}
		b.WriteByte('\n')
		if c.Summary != "" {
			fmt.Fprintf(&b, "      %s\n", c.Summary)
		}
		if a := c.Advisory; a != nil {
			switch {
			case a.Error != "":
				fmt.Fprintf(&b, "      advisory (%s): no answer (%s); status unchanged\n", a.Source, a.Error)
			default:
				fmt.Fprintf(&b, "      advisory (%s): looks %s, confidence %.2f; status unchanged\n", a.Source, a.Class, a.Confidence)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func duration(c Check) string {
	if c.Started.IsZero() || c.Ended.IsZero() || c.Ended.Before(c.Started) {
		return "duration unknown"
	}
	return c.Ended.Sub(c.Started).Round(10 * time.Millisecond).String()
}

// labelOf folds a multi-line command onto one line and clips it.
func labelOf(cmd string) string {
	cmd = strings.Join(strings.Fields(cmd), " ")
	if r := []rune(cmd); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return cmd
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
