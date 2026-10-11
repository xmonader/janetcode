package tui

import "testing"

// A new turn starts with no cost line: one that ended without its done
// event used to show the turn before's cost and tokens as its own.
func TestStartTurnForgetsTheLastTurnsCost(t *testing.T) {
	h := newHarness(t)
	h.m.lastTurn, h.m.lastTurnTokens = "$0.42", "in 10k · out 2k"
	h.m.startTurn("thinking")
	if h.m.lastTurn != "" || h.m.lastTurnTokens != "" {
		t.Fatalf("stale cost kept: %q %q", h.m.lastTurn, h.m.lastTurnTokens)
	}
}
