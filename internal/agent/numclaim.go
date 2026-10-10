package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/numclaim"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// Checks on numeric claims (see package numclaim). The verdict holds "tests
// pass" against the turn's checks; this holds "40% faster", "allocations
// halved" and "coverage 85%" against the session's measurements. A number
// in the answer that no benchmark, profile or coverage run backs — or a
// speed change with no run before the edits to compare with — is named in
// a notice beside the answer, and on the session log, the way the verdict
// line names an unverified "tests pass".
//
// The notice is deterministic and costs no model call, so it is on by
// default; JANETCODE_OFF=numclaim turns it off. Sending the turn back to
// measure or retract the number is a model call, so that part is opt-in
// (numeric_claims_nudge) and shares the verdict's one correction a turn.

// numclaimSteps reduces a session's events to what numclaim checks
// against: each command with its output, and each edit, in order. A call
// the harness refused never ran and is left out; a gate's command counts
// as a command the session ran.
func numclaimSteps(events []session.Event, root string) []numclaim.Step {
	var out []numclaim.Step
	for _, e := range events {
		switch {
		case e.Type == session.TypeGate:
			out = append(out, numclaim.Step{Cmd: strings.TrimSpace(e.Name), Output: e.Output, Failed: e.IsError})
		case e.Type != session.TypeToolResult || refused(e):
		case editTool(e.Name):
			if !e.IsError {
				out = append(out, numclaim.Step{Edit: true})
			}
		case e.Name == "bash" && !neverRan(e):
			cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
			out = append(out, numclaim.Step{Cmd: cmd, Output: e.Output, Failed: runFailed(e)})
		}
	}
	return out
}

// numclaimFindings checks the numbers in an answer against the session so
// far. Nil when the answer states none the checker can judge.
func numclaimFindings(text string, events []session.Event, root string) []numclaim.Finding {
	cs := numclaim.Extract(text)
	if len(cs) == 0 {
		return nil
	}
	return numclaim.Check(cs, numclaimSteps(events, root))
}

// numclaimGate notes the numbers in the turn's answer that nothing
// measured and, with numeric_claims_nudge on, sends the turn back once to
// measure them or say they are estimates. It reports whether the turn
// goes on.
func (a *Agent) numclaimGate(text string, g *turnGates, emit func(Event)) bool {
	if a.Off["numclaim"] || a.Log == nil || strings.TrimSpace(text) == "" {
		return false
	}
	fs := numclaimFindings(text, a.Log.Events(), a.displayRoot())
	if len(fs) == 0 {
		return false
	}
	n := numclaim.Unbacked(fs)
	raw, _ := json.Marshal(fs)
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "numclaim", IsError: n > 0,
		Output: fmt.Sprintf("%d of %d number(s) in the answer backed by a measurement", len(fs)-n, len(fs)), Extra: raw})
	if n == 0 {
		return false
	}
	emit(Event{Kind: EvNotice, Text: "unverified numbers in the answer: " + numclaim.Summary(fs)})
	if g.conflicted || !a.NumericClaimsNudgeOn() || g.corrections > 0 {
		return false
	}
	g.corrections++
	a.appendHarnessText(numclaimNote(fs))
	return true
}

// numclaimNote is what the model is told: the verdict's voice — the
// evidence, the measurement that would settle it, and room to retract.
func numclaimNote(fs []numclaim.Finding) string {
	var b strings.Builder
	b.WriteString("[harness] Your answer states numbers that no measurement in this session backs:\n")
	for _, f := range fs {
		if !f.OK() {
			fmt.Fprintf(&b, "- %q (%s): %s\n", f.Claim.Text, f.Claim.Kind, f.Why)
		}
	}
	b.WriteString("Measure them — for a speed or memory change, a run before the change and one after (a benchmark, benchstat or hyperfine), for coverage a coverage run — and report what it printed, or say plainly that the numbers are estimates.")
	return b.String()
}

// SetNumericClaimsNudge turns the numeric-claims send-back on or off.
func (a *Agent) SetNumericClaimsNudge(on bool) {
	a.featMu.Lock()
	a.NumericClaimsNudge = on
	a.featMu.Unlock()
}

// NumericClaimsNudgeOn is NumericClaimsNudge, safe to read while a turn
// runs.
func (a *Agent) NumericClaimsNudgeOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.NumericClaimsNudge
}
