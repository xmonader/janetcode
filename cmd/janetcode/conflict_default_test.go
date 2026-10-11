package main

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/prompt"
)

// CONFLICT: is offered unless the config turns it off, and its rule sits
// with the standing rules, above the environment and any appended text.
func TestConflictExitIsOnByDefault(t *testing.T) {
	var c config.Config
	if !c.ConflictExitEnabled() {
		t.Fatal("conflict_exit should be on when unset")
	}
	off := false
	c.ConflictExit = &off
	if c.ConflictExitEnabled() {
		t.Fatal("conflict_exit=false should turn it off")
	}
	sys := withConflictRule("rules\n" + prompt.EnvironmentHeading + "- Date: x\n\nAnswer in French.")
	i, j := strings.Index(sys, agent.ConflictPrompt), strings.Index(sys, prompt.EnvironmentHeading)
	if i < 0 || i > j || !strings.HasSuffix(sys, "Answer in French.") {
		t.Fatalf("rule misplaced:\n%s", sys)
	}
}
