package agent

import (
	"github.com/xmonader/janetcode/internal/evidence"
)

// EvidenceReport is the session's verification evidence as it stands now,
// across every turn: each check with its status, exit code, timing and
// freshness against the latest edit, and the final status that says
// whether a success claim is backed. /status, the web UI and anything
// else that asks "is this work verified right now" read this, and the
// per-turn verdicts and the proof notes fold the same log the same way.
func (a *Agent) EvidenceReport() evidence.Report {
	if a.Log == nil {
		return evidence.Report{Status: evidence.NotRun, Why: "no session log to judge from"}
	}
	rep := evidence.FromEvents(a.logEvents(), evidenceOptions(0, a.displayRoot()))
	// A red scratch script the model replaced with the real command is
	// not a failure of the code (see retiredChecks).
	status := map[string]evidence.Status{}
	order := make([]string, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		order = append(order, c.Command)
		status[c.Command] = c.Status
	}
	retired := retiredChecks(order, func(c string) bool { return status[c] == evidence.Failed }, scratchScript)
	return rep.Without(retired)
}
