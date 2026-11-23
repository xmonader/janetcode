package spend

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is an injectable local time, so rollover is tested without
// waiting for midnight.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// A second Ledger on the same path is a restart, or another process: it
// must see what the first recorded.
func TestLedgerAccumulatesAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", File)
	c := &clock{t: at("2026-11-23 10:00")}
	if err := New(path, c.now).Add(1.25); err != nil {
		t.Fatal(err)
	}
	b := New(path, c.now)
	if err := b.Add(0.75); err != nil {
		t.Fatal(err)
	}
	day, month, err := New(path, c.now).Totals()
	if err != nil || !near(day, 2) || !near(month, 2) {
		t.Fatalf("totals = %v %v %v", day, month, err)
	}
}

func TestLedgerRollsOverDayAndMonth(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	c := &clock{t: at("2026-11-29 23:59")}
	l := New(path, c.now)
	l.Add(3)
	c.set(at("2026-11-30 00:01"))
	l.Add(1)
	if day, month, _ := l.Totals(); !near(day, 1) || !near(month, 4) {
		t.Fatalf("next day: day %v month %v", day, month)
	}
	c.set(at("2026-12-01 09:00"))
	if day, month, _ := l.Totals(); day != 0 || month != 0 {
		t.Fatalf("next month starts empty: day %v month %v", day, month)
	}
	l.Add(2)
	// Going back sees the old month's figure again: nothing was erased.
	c.set(at("2026-11-30 12:00"))
	if day, month, _ := l.Totals(); !near(day, 1) || !near(month, 4) {
		t.Fatalf("history kept: day %v month %v", day, month)
	}
}

func TestLedgerPrunesOldDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	c := &clock{t: at("2026-01-01 10:00")}
	l := New(path, c.now)
	for i := 0; i < keepDays+10; i++ {
		l.Add(0.01)
		c.set(c.now().AddDate(0, 0, 1))
	}
	d, err := l.read()
	if err != nil || len(d.Days) != keepDays {
		t.Fatalf("days kept = %d, err %v", len(d.Days), err)
	}
}

func TestCheckRefusesAtTheCapNamingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	c := &clock{t: at("2026-11-23 10:00")}
	l := New(path, c.now)
	caps := Caps{Daily: 2, Monthly: 10}
	if err := l.Check(caps); err != nil {
		t.Fatalf("empty ledger refused: %v", err)
	}
	l.Add(1.99)
	if err := l.Check(caps); err != nil {
		t.Fatalf("under the cap refused: %v", err)
	}
	l.Add(0.01)
	err := l.Check(caps)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Period != "daily" || !near(ce.Spent, 2) {
		t.Fatalf("want a daily CapError, got %v", err)
	}
	for _, want := range []string{"daily spend cap", "$2.00", "today", "spend.daily_usd"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// Tomorrow the daily cap is clear; the monthly one is the next limit.
	c.set(at("2026-11-24 10:00"))
	if err := l.Check(caps); err != nil {
		t.Fatalf("new day still refused: %v", err)
	}
	l.Add(8)
	caps.Daily = 0
	if err := l.Check(caps); !errors.As(err, &ce) || ce.Period != "monthly" {
		t.Fatalf("want a monthly CapError, got %v", err)
	}
	// Zero is off.
	if err := l.Check(Caps{}); err != nil {
		t.Fatalf("no caps refused: %v", err)
	}
}

func TestConcurrentWritersLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	c := &clock{t: at("2026-11-23 10:00")}
	const writers, each = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := New(path, c.now) // its own instance: another process
			for i := 0; i < each; i++ {
				if err := l.Add(0.5); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	day, month, err := New(path, c.now).Totals()
	want := float64(writers*each) * 0.5
	if err != nil || !near(day, want) || !near(month, want) {
		t.Fatalf("totals = %v %v %v, want %v", day, month, err, want)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("lock file left behind: %v", err)
	}
}

// A stale lock from a dead process must not stop spend being recorded.
func TestStaleLockIsBroken(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(path+".lock", old, old)
	if err := New(path, nil).Add(1); err != nil {
		t.Fatal(err)
	}
}

// A corrupt file starts over instead of wedging the feature, and an
// unreadable one never refuses a call.
func TestCorruptLedgerFailsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), File)
	os.WriteFile(path, []byte("{not json"), 0o600)
	l := New(path, nil)
	if err := l.Check(Caps{Daily: 1}); err != nil {
		t.Fatalf("corrupt file refused a call: %v", err)
	}
	if err := l.Add(0.5); err != nil {
		t.Fatal(err)
	}
	if day, _, _ := l.Totals(); !near(day, 0.5) {
		t.Fatalf("day = %v", day)
	}
}

func TestNilGuardIsOff(t *testing.T) {
	var g *Guard
	if g.Check() != nil || g.Add(1) != nil || g.Summary() != "" {
		t.Fatal("a nil guard must do nothing")
	}
}

func TestDefaultPathFollowsXDGStateHome(t *testing.T) {
	env := map[string]string{"XDG_STATE_HOME": "/s", "HOME": "/h"}
	p, err := DefaultPath(func(k string) string { return env[k] })
	if err != nil || p != filepath.Join("/s", "janetcode", File) {
		t.Fatalf("path = %q, %v", p, err)
	}
	delete(env, "XDG_STATE_HOME")
	p, _ = DefaultPath(func(k string) string { return env[k] })
	if p != filepath.Join("/h", ".local", "state", "janetcode", File) {
		t.Fatalf("fallback path = %q", p)
	}
}
