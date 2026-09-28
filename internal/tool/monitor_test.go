package tool

import (
	"context"
	"strings"
	"testing"
	"time"
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

// The watch reads everything the job prints, not the last screenful at
// each poll: it used to look at the last 80 lines every quarter second,
// so a match followed quickly by more output — a server's "ready" and
// then its request log, a test's FAIL and then the rest of the run —
// scrolled out of view unseen and the monitor never fired.
func TestMonitorSeesAMatchThatScrolledPast(t *testing.T) {
	got := make(chan string, 1)
	m := Monitor{Jobs: &Jobs{Dir: t.TempDir()}, Root: t.TempDir(), OnMatch: func(s string) { got <- s }}
	defer m.Jobs.KillAll()
	if _, err := m.Run(context.Background(), raw(`{"command":"echo server ready; i=0; while [ $i -lt 300 ]; do echo log line $i; i=$((i+1)); done; sleep 5","pattern":"ready"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if !strings.Contains(s, "server ready") {
			t.Fatalf("match = %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the match scrolled past and was never reported")
	}
}
