package offpeak

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// live is DeepSeek's old window with no end date, as a table for tests
// that need a window in force.
var live = Table{{Provider: "deepseek", Models: []string{"deepseek-chat"}, Start: "16:30", End: "00:30", Multiplier: 0.5}}

func TestMultiplierAcrossMidnight(t *testing.T) {
	for _, c := range []struct {
		when string
		want float64
	}{
		{"2026-10-10T16:29:00Z", 1},
		{"2026-10-10T16:30:00Z", 0.5},
		{"2026-10-10T23:59:00Z", 0.5},
		{"2026-10-11T00:29:00Z", 0.5},
		{"2026-10-11T00:30:00Z", 1},
		{"2026-10-11T03:56:00+02:00", 1}, // 01:56 UTC: r13's start
	} {
		if got := live.Multiplier("deepseek/deepseek-chat", at(c.when)); got != c.want {
			t.Errorf("%s: %v, want %v", c.when, got, c.want)
		}
	}
	if got := live.Multiplier("xiaomi/mimo-v2.6-flash", at("2026-10-10T20:00:00Z")); got != 1 {
		t.Fatalf("a model no window names is full price: %v", got)
	}
}

// TestDefaultIsExpired: the built-in DeepSeek rows are the record of a
// discount that ended, so today they price nothing — but still say the
// model was covered, which is a different answer from "never discounted".
func TestDefaultIsExpired(t *testing.T) {
	d := Default()
	if m := d.Multiplier("deepseek-chat", at("2025-09-01T20:00:00Z")); m != 0.5 {
		t.Fatalf("before it ended: %v", m)
	}
	if m := d.Multiplier("deepseek-reasoner", at("2025-09-01T20:00:00Z")); m != 0.25 {
		t.Fatalf("reasoner before it ended: %v", m)
	}
	if m := d.Multiplier("deepseek-chat", at("2026-10-10T20:00:00Z")); m != 1 {
		t.Fatalf("after it ended: %v", m)
	}
	if !d.Covers("deepseek/deepseek-chat") || d.Covers("mimo") {
		t.Fatal("covers")
	}
}

func TestScheduleWaitsForTheWindowWhenTheDeadlineAllows(t *testing.T) {
	now := at("2026-10-10T10:00:00Z")
	p := live.Schedule("deepseek-chat", now, at("2026-10-11T08:00:00Z"), 4*time.Hour)
	if !p.Start.Equal(at("2026-10-10T16:30:00Z")) || p.Multiplier != 0.5 || p.Now != 1 {
		t.Fatalf("plan %+v", p)
	}
	if p.Wait(now) != 6*time.Hour+30*time.Minute {
		t.Fatalf("wait %s", p.Wait(now))
	}
}

// TestScheduleDeadlineWins: a run that cannot fit the window before the
// deadline starts where it costs least while still finishing in time —
// partly in the window, or now.
func TestScheduleDeadlineWins(t *testing.T) {
	now := at("2026-10-10T10:00:00Z")
	// Must finish by 18:00: the best is to start at 14:00 and catch 1.5h
	// of the window — not to wait for 16:30 and miss the deadline.
	p := live.Schedule("deepseek-chat", now, at("2026-10-10T18:00:00Z"), 4*time.Hour)
	if !p.Start.Equal(at("2026-10-10T14:00:00Z")) || p.Multiplier >= 1 {
		t.Fatalf("plan %+v", p)
	}
	// A run a little over four hours must start a minute earlier: rounding
	// its length down to whole minutes planned a finish past the deadline.
	p = live.Schedule("deepseek-chat", now, at("2026-10-10T18:00:00Z"), 4*time.Hour+30*time.Second)
	if end := p.Start.Add(4*time.Hour + 30*time.Second); end.After(at("2026-10-10T18:00:00Z")) {
		t.Fatalf("plan %+v finishes at %s, past the deadline", p, end)
	}
	// A deadline that leaves no room is "start now".
	for _, dl := range []string{"2026-10-10T09:00:00Z", "2026-10-10T12:00:00Z"} {
		if p := live.Schedule("deepseek-chat", now, at(dl), 4*time.Hour); !p.Start.Equal(now) {
			t.Fatalf("deadline %s: %+v", dl, p)
		}
	}
	// Already inside the window: no reason to wait.
	in := at("2026-10-10T17:00:00Z")
	if p := live.Schedule("deepseek-chat", in, at("2026-10-11T17:00:00Z"), time.Hour); !p.Start.Equal(in) || p.Multiplier != 0.5 {
		t.Fatalf("in window: %+v", p)
	}
	// No window for the model: start now at full price.
	if p := live.Schedule("mimo", now, at("2026-10-11T08:00:00Z"), time.Hour); !p.Start.Equal(now) || p.Multiplier != 1 {
		t.Fatalf("uncovered: %+v", p)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if tb, err := Load(filepath.Join(dir, "missing.json")); err != nil || len(tb) != len(Default()) {
		t.Fatalf("missing file is the default: %v %d", err, len(tb))
	}
	path := filepath.Join(dir, "offpeak.json")
	os.WriteFile(path, []byte(`[{"provider":"x","models":["m"],"start":"22:00","end":"06:00","multiplier":0.4}]`), 0o644)
	tb, err := Load(path)
	if err != nil || len(tb) != 1 || tb.Multiplier("m", at("2026-10-10T23:00:00Z")) != 0.4 {
		t.Fatalf("%v %+v", err, tb)
	}
	for body, want := range map[string]string{
		`[{"models":["m"],"start":"25:00","end":"06:00","multiplier":0.4}]`:                        "HH:MM",
		`[{"models":["m"],"start":"22:00","end":"06:00","multiplier":1.5}]`:                        "fraction",
		`[{"models":[],"start":"22:00","end":"06:00","multiplier":0.5}]`:                           "no models",
		`[{"models":["m"],"start":"22:00","end":"06:00","multiplier":0.5,"until":"next tuesday"}]`: "cannot parse",
	} {
		os.WriteFile(path, []byte(body), 0o644)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", body, err)
		}
	}
}
