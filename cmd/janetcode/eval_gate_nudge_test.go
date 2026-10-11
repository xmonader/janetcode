package main

import (
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
)

// TestWireEvalGateCarriesTheStaleCheckNudge is the regression for a flag
// that was named in a measured arm while the eval agent never set it: an
// arm that asks for the nudge has to get it, and an arm that does not must
// stay byte-identical to one that never heard of it.
func TestWireEvalGateCarriesTheStaleCheckNudge(t *testing.T) {
	for _, want := range []bool{false, true} {
		ag := &agent.Agent{}
		wireEvalGate(ag, t.TempDir(), &config.Config{StaleCheckNudge: want}, map[string]bool{"decisions": true}, "")
		if ag.StaleCheckNudge != want {
			t.Errorf("config stale_check_nudge=%v reached the eval agent as %v", want, ag.StaleCheckNudge)
		}
		ag = &agent.Agent{}
		wireEvalGate(ag, t.TempDir(), &config.Config{EffortControl: want}, map[string]bool{"decisions": true}, "")
		if ag.EffortControl != want {
			t.Errorf("config effort_control=%v reached the eval agent as %v", want, ag.EffortControl)
		}
	}
}
