package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/offpeak"
)

func TestOffPeakDeadline(t *testing.T) {
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"2026-10-11T08:00:00Z": time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC),
		"8h":                   now.Add(8 * time.Hour),
		"12:30":                time.Date(2026, 10, 10, 12, 30, 0, 0, time.UTC),
		"08:00":                time.Date(2026, 10, 11, 8, 0, 0, 0, time.UTC), // already past today
	} {
		if got, err := offPeakDeadline(in, now); err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"tomorrow", "-2h", ""} {
		if _, err := offPeakDeadline(bad, now); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// TestOffPeakWaitSaysWhatItDoes: each of the four outcomes is one line on
// stderr, and only a later start waits.
func TestOffPeakWaitSaysWhatItDoes(t *testing.T) {
	live := offpeak.Table{{Models: []string{"deepseek-chat"}, Start: "16:30", End: "00:30", Multiplier: 0.5}}
	now := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	var waited time.Duration
	after := func(d time.Duration) <-chan time.Time {
		waited = d
		c := make(chan time.Time, 1)
		c <- now.Add(d)
		return c
	}
	for _, c := range []struct {
		table offpeak.Table
		model string
		now   time.Time
		want  string
		wait  time.Duration
	}{
		{live, "deepseek-chat", now, "waiting 6h30m", 6*time.Hour + 30*time.Minute},
		{live, "deepseek-chat", now.Add(8 * time.Hour), "in a discounted window now", 0},
		{offpeak.Default(), "deepseek-chat", now, "have ended", 0},
		{live, "mimo", now, "names no off-peak window", 0},
	} {
		waited = 0
		var b bytes.Buffer
		if err := offPeakWait(context.Background(), &b, c.table, c.model, c.now, now.Add(24*time.Hour), time.Hour, after); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), c.want) || waited != c.wait {
			t.Errorf("%s: waited %s, said %q", c.model, waited, b.String())
		}
	}
	// ctrl-c during the wait ends it with the context's error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	never := func(time.Duration) <-chan time.Time { return nil }
	var b bytes.Buffer
	if err := offPeakWait(ctx, &b, live, "deepseek-chat", now, now.Add(24*time.Hour), time.Hour, never); err == nil {
		t.Fatal("a cancelled wait returned nil")
	}
}
