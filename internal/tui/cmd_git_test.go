package tui

import (
	"strings"
	"testing"
)

// A draft never overwrites what the user has typed since asking for it.
func TestGitflowDraftKeepsTypedInput(t *testing.T) {
	h := newHarness(t)
	h.keys("half a thought")
	msg := gitflowMsg{}
	msg.Compose = "/commit -m x"
	h.update(msg)
	if got := h.m.input.Value(); got != "half a thought" {
		t.Fatalf("input = %q", got)
	}
	if !strings.Contains(plain(h.m.View()), "/commit -m x") {
		t.Fatal("the draft was not shown in the transcript instead")
	}
}
