package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/offpeak"
)

// offPeakMode is --off-peak-by: wait for the provider's cheapest hours
// that still let the run finish by the deadline (internal/offpeak), then
// carry on with -p or --eval — or, given neither, exit 0 so a script can
// put `janetcode --off-peak-by 08:00 --model M &&` in front of any batch
// job, a benchmark campaign included. Opt-in, being a flag: it decides
// when the work happens.
func offPeakMode(o *options, cfg *config.Config) (bool, error) {
	if o.offPeakBy == "" {
		return false, nil
	}
	model := o.model
	if cfg != nil && cfg.Model != "" {
		model = cfg.Model
	}
	now := time.Now()
	deadline, err := offPeakDeadline(o.offPeakBy, now)
	if err != nil {
		return true, headless.BadInput("--off-peak-by: %v", err)
	}
	table := offpeak.Default()
	if p, err := config.Path(); err == nil {
		if table, err = offpeak.Load(filepath.Join(filepath.Dir(p), "offpeak.json")); err != nil {
			return true, err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := offPeakWait(ctx, os.Stderr, table, model, now, deadline, o.offPeakEst, time.After); err != nil {
		return true, err
	}
	// Nothing to run after the wait: the wait was the job.
	return !o.headless() && o.evalPath == "", nil
}

// offPeakWait prints the plan and sleeps until it starts. after is
// time.After, a parameter so a test does not wait.
func offPeakWait(ctx context.Context, w io.Writer, table offpeak.Table, model string, now, deadline time.Time, est time.Duration, after func(time.Duration) <-chan time.Time) error {
	p := table.Schedule(model, now, deadline, est)
	wait := p.Wait(now)
	switch {
	case wait > 0:
		fmt.Fprintf(w, "[off-peak] %s costs ×%.2f of peak from %s (×%.2f now); waiting %s, finishing by %s\n",
			model, p.Multiplier, p.Start.Local().Format("15:04"), p.Now, wait.Round(time.Minute), deadline.Local().Format("Mon 15:04"))
	case p.Now < 1:
		fmt.Fprintf(w, "[off-peak] %s is in a discounted window now (×%.2f); starting\n", model, p.Now)
	case table.Covers(model):
		fmt.Fprintf(w, "[off-peak] no discount for %s is in force before %s (the table's windows for it have ended or do not fit); starting now\n", model, deadline.Local().Format("Mon 15:04"))
	default:
		fmt.Fprintf(w, "[off-peak] the table names no off-peak window for %s; starting now\n", model)
	}
	if wait <= 0 {
		return nil
	}
	select {
	case <-after(wait):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// offPeakDeadline reads --off-peak-by: an RFC 3339 instant, a local time
// of day ("08:00", the next one to come), or a duration from now ("8h").
func offPeakDeadline(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(d), nil
	}
	if c, err := time.ParseInLocation("15:04", s, now.Location()); err == nil {
		t := time.Date(now.Year(), now.Month(), now.Day(), c.Hour(), c.Minute(), 0, 0, now.Location())
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%q is not a deadline: give an RFC 3339 time, a local HH:MM, or a duration such as 8h", s)
}
