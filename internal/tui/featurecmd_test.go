package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
)

// /features <consumer> on while jev itself is off answered "off" with no
// reason, right after being told to turn it on.
func TestFeatureConsumerSaysJevIsOff(t *testing.T) {
	h := newHarness(t)
	off := false
	cfg := &config.Config{Provider: "fake", Model: "m", Decisions: &config.DecisionsConfig{Enabled: &off}}
	h.withConfig(t, cfg, nil)
	h.m.command("/features triage on")
	if !cfg.Decisions.Triage {
		t.Fatal("triage was not switched on")
	}
	if !strings.Contains(h.m.status, "/jev on") {
		t.Fatalf("status does not say jev is off: %q", h.m.status)
	}
}
