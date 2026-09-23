package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// /routing is the trust control for step routing (github issue #4): one
// command vetoes the router for the rest of the session, a second lifts
// the veto, and each says what it did.
func TestRoutingCommandVetoesStepRouting(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "seen"), StopReason: llm.StopEndTurn})
	if h.m.opts.Agent.RoutingOff() {
		t.Fatal("routing starts on")
	}

	h.keys("/routing")
	h.enter()
	if !h.m.opts.Agent.RoutingOff() {
		t.Fatal("/routing must veto step routing")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "step routing off") {
		t.Fatalf("the veto must say what it did:\n%s", v)
	}

	h.keys("/routing")
	h.enter()
	if h.m.opts.Agent.RoutingOff() {
		t.Fatal("the second /routing must lift the veto")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "step routing on") {
		t.Fatalf("lifting the veto must say so:\n%s", v)
	}
}
