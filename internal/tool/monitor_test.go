package tool

import (
	"context"
	"strings"
	"testing"
)

// monitor runs a shell command exactly as bash does, so it gets bash's
// gates: the catastrophic refusal, and the Guard that asks even under
// auto-approve. Without them, the command bash refuses — or one that
// writes outside the workspace — ran unasked when it came with a pattern.
func TestMonitorHasBashsGates(t *testing.T) {
	root := t.TempDir()
	m := Monitor{Jobs: &Jobs{Dir: t.TempDir()}, Root: root}
	defer m.Jobs.KillAll()
	_, err := m.Run(context.Background(), raw(`{"command":"echo hi && sudo rm -rf /","pattern":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v", err)
	}
	if len(m.Jobs.List()) != 0 {
		t.Fatal("a refused command was started")
	}
	var _ Guard = m
	if ask, why := m.Guard(raw(`{"command":"cp a.txt ../elsewhere/","pattern":"x"}`)); !ask || !strings.Contains(why, "outside the workspace") {
		t.Fatalf("guard = %v %q", ask, why)
	}
	if ask, _ := m.Guard(raw(`{"command":"tail -f log.txt","pattern":"x"}`)); ask {
		t.Fatal("an ordinary command should not ask")
	}
	if w := m.Warn(raw(`{"command":"rm -rf ../x","pattern":"x"}`)); !strings.Contains(w, "recursive force delete") {
		t.Fatalf("warn = %q", w)
	}
}
