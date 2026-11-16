package webui

import (
	"strings"
	"testing"
	"time"
)

// A switch that lands between a subagent's "is the page still on my
// session?" check and its report must not leave the report on the new
// session's page. The seam runs the switch exactly there and gives it
// time to finish: unguarded it does, and the notice then lands on B.
func TestSubagentReportAndSessionSwitchAreAtomic(t *testing.T) {
	e := newSwitchEnv(t, &holdProv{started: make(chan struct{}, 1), release: make(chan struct{})})
	s := e.s
	switched := make(chan struct{})
	s.beforeTaskReport = func() {
		go func() {
			s.setSession(e.b)
			s.resetView()
			close(switched)
		}()
		select {
		case <-switched: // unguarded: the switch won the race
		case <-time.After(100 * time.Millisecond): // guarded: it waits for the report
		}
	}
	s.reportTask(e.a, "agent", "agent finished · atomic probe", false)
	select {
	case <-switched:
	case <-time.After(5 * time.Second):
		t.Fatal("the session switch never completed")
	}
	if s.sess() != e.b {
		t.Fatal("server did not move to B")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, en := range s.entries {
		if strings.Contains(en.Text, "atomic probe") {
			t.Fatalf("A's subagent notice is on B's page: %+v", en)
		}
	}
}
