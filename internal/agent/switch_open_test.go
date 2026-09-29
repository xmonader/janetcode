package agent

import (
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

// /clear and /resume switch sessions through OpenSession, which moved the
// log, pins and undo stack but not the /accept state or the armed /loop
// jobs: the old session's accept lock went on refusing edits in the new
// one, its loops went on firing prompts into it, and every later /accept
// or /loop change was saved to the session left behind. The incoming
// session's own state takes over instead, as it does at startup.
func TestOpenSessionSwitchesAcceptAndLoops(t *testing.T) {
	dir := t.TempDir()
	old, err := session.New(dir, "/proj", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newAgent(t, &fake{}, nil)
	a.SetLog(old)
	a.SetAccept(nil, func(am *session.AcceptMeta) { _ = old.SetAccept(am) })
	a.Cron = NewCron()
	defer a.Cron.Close()
	a.Cron.SetOnChange(func(specs []session.LoopSpec) { _ = old.SetLoops(specs) })
	a.AcceptStart("add a foo endpoint", "/proj")
	if _, err := a.Cron.Add(10*time.Minute, "check the build"); err != nil {
		t.Fatal(err)
	}

	// A fresh session (/clear): nothing of the old state is live.
	fresh, err := session.New(dir, "/proj", "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenSession(fresh, nil); err != nil {
		t.Fatal(err)
	}
	if a.AcceptMeta() != nil {
		t.Errorf("the old session's /accept state is live in the new one: %+v", a.AcceptMeta())
	}
	if jobs := a.Cron.List(); len(jobs) != 0 {
		t.Errorf("the old session's loops are still armed: %+v", jobs)
	}
	if len(old.Meta.Loops) != 1 || old.Meta.Accept == nil {
		t.Errorf("leaving a session changed its saved state: loops=%v accept=%v", old.Meta.Loops, old.Meta.Accept)
	}
	a.AcceptStart("add a bar endpoint", "/proj")
	if _, err := a.Cron.Add(5*time.Minute, "run the tests"); err != nil {
		t.Fatal(err)
	}
	if fresh.Meta.Accept == nil || len(fresh.Meta.Loops) != 1 {
		t.Errorf("changes after /clear were not saved to the new session: accept=%v loops=%v", fresh.Meta.Accept, fresh.Meta.Loops)
	}
	if old.Meta.Accept.Request != "add a foo endpoint" || len(old.Meta.Loops) != 1 || old.Meta.Loops[0].Prompt != "check the build" {
		t.Errorf("changes after /clear were saved to the old session: accept=%+v loops=%v", old.Meta.Accept, old.Meta.Loops)
	}

	// Resuming the old session (/resume) brings its own state back.
	if err := a.OpenSession(old, nil); err != nil {
		t.Fatal(err)
	}
	if am := a.AcceptMeta(); am == nil || am.Request != "add a foo endpoint" {
		t.Errorf("resumed /accept state = %+v, want the old session's", am)
	}
	if jobs := a.Cron.List(); len(jobs) != 1 || jobs[0].Prompt != "check the build" {
		t.Errorf("resumed loops = %+v, want the old session's one", jobs)
	}
}
