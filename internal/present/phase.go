package present

import "github.com/xmonader/janetcode/internal/tool"

// The phase is the few words the status line spends on what the turn is
// doing right now. "thinking" is the idle wait, which the front-ends
// dress with a playful verb (flavor.ForPhase); every other phase is said
// as it is. The terminal said "running bash" where the browser said
// "bash go test ./…", and only the terminal said "thinking" when the
// model was reasoning; both say the same now.
const (
	PhaseThinking = "thinking"
	PhaseWriting  = "writing"
)

// phaseSummaryWidth bounds a tool's summary inside the phase: the status
// line also carries the elapsed time and the hints.
const phaseSummaryWidth = 60

// PhasePreparing is the model writing a tool call's arguments, before any
// of them can be read.
func PhasePreparing(name string) string { return "preparing " + name }

// PhaseStreaming is the model part way through a tool call's arguments:
// what is being written, and how much so far. ok is false while nothing
// readable has arrived.
func PhaseStreaming(name, partial, cwd string) (string, bool) {
	s := tool.StreamingSummary(name, partial, func(p string) string { return RelPath(cwd, p) })
	if s == "" || s == "…" {
		return "", false
	}
	return name + " " + s, true
}

// PhaseRunning is a tool call running: the tool and what it does, so
// "bash go test ./…" rather than "running bash". A subagent is waited on.
func PhaseRunning(name, input, cwd string) string {
	if name == "task" {
		return "waiting on subagent"
	}
	sum := ToolSummary(name, input, phaseSummaryWidth, cwd)
	if sum == "" {
		return name
	}
	return name + " " + sum
}
