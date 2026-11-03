package cron

import (
	"strings"
	"testing"
	"time"
)

func TestMandateChangedNoticeOnce(t *testing.T) {
	c := New()
	defer c.Close()
	cur := &Posture{Trust: "full", Mode: "ask"}
	c.SetPosture(func() Posture { return *cur })
	var notes []string
	c.SetOnNotice(func(s string) { notes = append(notes, s) })
	c.SetOnFire(func(string, string) {})
	id, _ := c.Add(time.Hour, "tidy")
	e := c.jobs[id]
	cur.Mode = "allow-all"
	c.fire(id, e, KindFired, 0)
	c.fire(id, e, KindFired, 0)
	if len(notes) != 1 || !strings.Contains(notes[0], "/loop approve "+id) || !strings.Contains(notes[0], "mode ask -> allow-all") {
		t.Fatalf("want one notice, got %q", notes)
	}
	_ = c.Reapprove(id)
	cur.Allow = []string{"bash(extra)"}
	c.fire(id, e, KindFired, 0)
	if len(notes) != 2 {
		t.Fatalf("widened again after re-approval should notify again, got %d", len(notes))
	}
}
