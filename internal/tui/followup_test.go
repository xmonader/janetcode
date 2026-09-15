package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTabAcceptsFollowupGhostText(t *testing.T) {
	h := newHarness(t)
	h.m.followupOn = true
	h.m.followup = "run the tests next"
	h.m.layout()
	if ph := h.m.placeholder(); !strings.Contains(ph, "run the tests next") {
		t.Fatalf("placeholder = %q", ph)
	}
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if h.m.input.Value() != "run the tests next" {
		t.Fatalf("input = %q", h.m.input.Value())
	}
	if h.m.followup != "" {
		t.Fatalf("ghost text still set: %q", h.m.followup)
	}
}

func TestFollowupOffByDefault(t *testing.T) {
	h := newHarness(t)
	if h.m.followupOn {
		t.Fatal("follow-up should be off unless configured")
	}
	h.keys("/followup")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "off") {
		t.Fatalf("/followup did not say it is off:\n%s", v)
	}
	h.keys("/followup on")
	h.enter()
	if !h.m.followupOn {
		t.Fatal("wanted on")
	}
}
