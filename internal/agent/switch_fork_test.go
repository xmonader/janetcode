package agent

import (
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

// TestForkSessionMovesAcceptAndLoopPersistence: after a plain /fork the
// fork owns the conversation, so an /accept or /loop change made after it
// must land on the fork's record, not on the session left behind.
func TestForkSessionMovesAcceptAndLoopPersistence(t *testing.T) {
	dir := t.TempDir()
	orig, err := session.New(dir, "/proj", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newAgent(t, &fake{}, nil)
	a.SetLog(orig)
	a.SetAccept(nil, func(am *session.AcceptMeta) { _ = orig.SetAccept(am) })
	a.Cron = NewCron()
	defer a.Cron.Close()
	a.Cron.SetOnChange(func(specs []session.LoopSpec) { _ = orig.SetLoops(specs) })

	ns, err := session.Fork(dir, orig, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ForkSession(ns); err != nil {
		t.Fatal(err)
	}

	a.AcceptStart("add a foo endpoint", "/proj")
	if _, err := a.Cron.Add(10*time.Minute, "check the build"); err != nil {
		t.Fatal(err)
	}
	if ns.Meta.Accept == nil {
		t.Errorf("fork's record has no accept state after /accept")
	}
	if orig.Meta.Accept != nil {
		t.Errorf("original session got the fork's accept state: %+v", orig.Meta.Accept)
	}
	if len(ns.Meta.Loops) != 1 {
		t.Errorf("fork's record loops = %v, want the one armed after the fork", ns.Meta.Loops)
	}
	if len(orig.Meta.Loops) != 0 {
		t.Errorf("original session got the fork's loop: %v", orig.Meta.Loops)
	}
}
