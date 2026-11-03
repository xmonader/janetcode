package main

import (
	"fmt"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/trust"
)

// currentMandate is the trust level and permission mode ag runs under now.
func currentMandate(ag *agent.Agent, gate *trust.Gate, cwd string) session.Mandate {
	return session.Mandate{Trust: gate.State(cwd).Level.String(), Mode: ag.PermMode()}
}

// trustRank orders trust levels by how much of the project is let in.
func trustRank(level string) int {
	switch level {
	case "untrusted":
		return 0
	case "instructions":
		return 2
	case "full":
		return 3
	}
	return 1 // undecided
}

// widerMandate says why now is wider than the mandate a run started under,
// or "" when it is the same or narrower. Wider means more runs without
// asking (a higher permission mode: agent.ModeRank) or more of the project
// let in (a higher trust level). A recovered run is refused rather than
// carried on under settings its starter never gave it: --recover would
// otherwise turn a readonly or untrusted run into a yolo one just because
// the flags or the stored trust changed while it was down.
func widerMandate(orig, now session.Mandate) string {
	var why []string
	if agent.ModeRank(now.Mode) > agent.ModeRank(orig.Mode) {
		why = append(why, fmt.Sprintf("it started in %s mode and the mode is now %s", orig.Mode, now.Mode))
	}
	if trustRank(now.Trust) > trustRank(orig.Trust) {
		why = append(why, fmt.Sprintf("it started with trust %s and the trust is now %s", orig.Trust, now.Trust))
	}
	if len(why) == 0 {
		return ""
	}
	return "the settings are wider than the run's own: " + why[0] + joinMore(why[1:])
}

func joinMore(more []string) string {
	if len(more) == 0 {
		return ""
	}
	return "; " + more[0]
}
