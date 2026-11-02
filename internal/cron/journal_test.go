package cron

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

func TestMissedPolicy(t *testing.T) {
	now := time.Now()
	h := time.Hour
	cases := []struct {
		name   string
		last   time.Time
		every  time.Duration
		missed int
		run    bool
	}{
		{"no history", time.Time{}, h, 0, false},
		{"less than one interval", now.Add(-30 * time.Minute), h, 0, false},
		{"three missed coalesce", now.Add(-3*h - time.Minute), h, 3, true},
		{"exactly a day still runs", now.Add(-24 * h), 10 * time.Minute, 144, true},
		{"stale beyond a day", now.Add(-48 * h), h, 48, false},
		{"future last", now.Add(h), h, 0, false},
	}
	for _, c := range cases {
		m, r := Missed(c.last, now, c.every)
		if m != c.missed || r != c.run {
			t.Errorf("%s: got %d,%v want %d,%v", c.name, m, r, c.missed, c.run)
		}
	}
}

func TestRestoreCatchesUpOnce(t *testing.T) {
	j := OpenJournal("")
	j.Append(Occurrence{Time: time.Now().Add(-5 * time.Hour), Job: "loop-1", Kind: KindFired})
	j.Append(Occurrence{Time: time.Now().Add(-5 * time.Hour), Job: "loop-2", Kind: KindAdded})
	j.Append(Occurrence{Time: time.Now().Add(-100 * time.Hour), Job: "loop-3", Kind: KindFired})
	c := New()
	defer c.Close()
	c.SetJournal(j)
	if err := c.Restore([]session.LoopSpec{
		{ID: "loop-1", Every: "1h", Prompt: "a"},
		{ID: "loop-3", Every: "1h", Prompt: "stale"},
		{ID: "loop-4", Every: "1h", Prompt: "new"}, // no history
	}); err != nil {
		t.Fatal(err)
	}
	var catchups, stale int
	for _, o := range j.Recent(50, "") {
		switch {
		case o.Job == "loop-1" && o.Kind == KindCatchup:
			catchups++
		}
	}
	_ = catchups
	infos := c.List()
	if got := infos[0].Next.Sub(time.Now()); got > 3*time.Second {
		t.Errorf("loop-1 should be due for catch-up soon, next in %v", got)
	}
	if got := infos[1].Next.Sub(time.Now()); got < 50*time.Minute {
		t.Errorf("stale loop-3 must keep its normal schedule, next in %v", got)
	}
	for _, o := range j.Recent(50, "loop-3") {
		if o.Kind == KindStale && o.Missed >= 100 {
			stale++
		}
	}
	if stale != 1 {
		t.Errorf("want one stale entry for loop-3, got %d", stale)
	}
	if _, ok := j.LastRun("loop-4"); !ok {
		t.Error("loop-4 should get a baseline entry")
	}
}

func TestCatchupFiresExactlyOnce(t *testing.T) {
	j := OpenJournal("")
	j.Append(Occurrence{Time: time.Now().Add(-5 * time.Hour), Job: "loop-1", Kind: KindFired})
	c := New()
	defer c.Close()
	c.SetJournal(j)
	got := make(chan string, 4)
	c.SetOnFire(func(id, p string) { got <- id })
	_ = c.Restore([]session.LoopSpec{{ID: "loop-1", Every: "1h", Prompt: "a"}})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no catch-up firing")
	}
	select {
	case <-got:
		t.Fatal("catch-up must not burst")
	case <-time.After(300 * time.Millisecond):
	}
	rec := j.Recent(10, "loop-1")
	last := rec[len(rec)-1]
	if last.Kind != KindCatchup || last.Missed != 5 {
		t.Errorf("last = %+v", last)
	}
}

func TestJournalFilePersistsAndCompacts(t *testing.T) {
	dir, err := os.MkdirTemp(".", "jtest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "s.cron.log")
	j := OpenJournal(p)
	for i := 0; i < journalMax+50; i++ {
		j.Append(Occurrence{Job: "loop-1", Kind: KindFired})
	}
	os.WriteFile(p, append(mustRead(t, p), []byte("garbage\n")...), 0o600)
	j2 := OpenJournal(p)
	if n := len(j2.Recent(10000, "")); n == 0 || n > journalMax+1 {
		t.Errorf("reloaded %d entries", n)
	}
	if _, ok := j2.LastRun("loop-1"); !ok {
		t.Error("lastrun lost across reopen")
	}
}

func mustRead(t *testing.T, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestToolHistory(t *testing.T) {
	c := New()
	defer c.Close()
	c.SetJournal(OpenJournal(""))
	tl := &Tool{Cron: c}
	if out, _ := tl.Run(t.Context(), []byte(`{"action":"history"}`)); !strings.Contains(out, "no occurrences") {
		t.Errorf("empty = %q", out)
	}
	id, _ := c.Add(time.Hour, "x")
	c.Cancel(id)
	out, err := tl.Run(t.Context(), []byte(`{"action":"history","id":"`+id+`"}`))
	if err != nil || !strings.Contains(out, "added") || !strings.Contains(out, "cancelled") {
		t.Errorf("history = %q %v", out, err)
	}
}
