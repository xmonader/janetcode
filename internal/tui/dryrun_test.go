package tui

import (
	"strings"
	"testing"
)

// /dryrun is not idle-only, and its apply ran as side work: startSideWork
// started a "turn" over the one already running, took over its cancel,
// and the side work's end marked the session idle while the real turn
// was still going — so esc no longer stopped it and the next prompt
// started a second turn on the same agent. off and discard append a
// harness note to the history the turn is writing. While a turn runs
// only the read-only forms run; the rest wait for it.
func TestDryrunChangesWaitForTheRunningTurn(t *testing.T) {
	blocker := &blocking{started: make(chan struct{})}
	opts := fakeOptions(t)
	opts.Agent.Provider = blocker
	h := newHarnessFor(t, opts)
	h.keys("slow")
	h.enter()
	<-blocker.started
	for _, line := range []string{"/dryrun apply", "/dryrun off", "/dryrun discard", "/dryrun"} {
		h.m.command(line)
		if !h.m.turn.running {
			t.Fatalf("%s ended the running turn's state", line)
		}
		if v := plain(h.m.View()); !strings.Contains(v, "waits for the turn to finish") {
			t.Fatalf("%s did not say it waits:\n%s", line, v)
		}
	}
	h.m.command("/dryrun show")
	if !h.m.turn.running {
		t.Fatal("/dryrun show ended the running turn's state")
	}
	h.m.cancelTurn()
	h.pump(func() bool { return !h.m.turn.running })
}
