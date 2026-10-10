// Package offpeak schedules headless work — a benchmark, a batch eval, a
// scripted -p run — into a provider's discounted hours, before a deadline.
// The same tokens bought in an off-peak window cost a fraction of the
// peak price and buy the same model, so for work nobody is waiting on the
// discount comes with no change in quality.
//
// Which providers discount, when, and by how much is data, not code: a
// Table of Windows, the built-in one (Default) overridden by a JSON file in
// the config directory, because providers change these terms — DeepSeek,
// the example everybody cites, ended its night discount in September 2025
// — and a schedule that hard-coded them would keep waiting for a discount
// that no longer exists. A window carries the dates it applies between, so
// an expired one stays in the table as a record and simply never matches.
package offpeak

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Window is one discounted period: every day from Start to End, UTC
// ("16:30" to "00:30" crosses midnight), the price multiplied by
// Multiplier, for the models whose name contains one of Models.
type Window struct {
	Provider string   `json:"provider"`
	Models   []string `json:"models"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	// Multiplier is the fraction of the peak price paid inside the window:
	// 0.5 is half price.
	Multiplier float64 `json:"multiplier"`
	// From and Until bound the dates the window applies, RFC 3339; empty is
	// unbounded. A provider that ends a discount gets an Until, not a
	// deleted row.
	From  string `json:"from,omitempty"`
	Until string `json:"until,omitempty"`
	Note  string `json:"note,omitempty"`
}

// Table is every window known.
type Table []Window

// Default is the built-in table. DeepSeek's off-peak discount (16:30–00:30
// UTC: 50% off deepseek-chat, 75% off deepseek-reasoner) is kept as the
// record of the terms the idea was priced on, with the date it ended.
func Default() Table {
	const ended = "2025-09-05T16:00:00Z"
	note := "DeepSeek's published off-peak discount; withdrawn with the V3.1 price change"
	return Table{
		{Provider: "deepseek", Models: []string{"deepseek-chat", "deepseek-v3"}, Start: "16:30", End: "00:30", Multiplier: 0.5, Until: ended, Note: note},
		{Provider: "deepseek", Models: []string{"deepseek-reasoner", "deepseek-r1"}, Start: "16:30", End: "00:30", Multiplier: 0.25, Until: ended, Note: note},
	}
}

// Load reads a table from path, a JSON array of windows. A missing file
// is the built-in table; a present one replaces it whole, so a row can be
// corrected or withdrawn without a release.
func Load(path string) (Table, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return nil, err
	}
	var t Table
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, w := range t {
		if err := w.check(); err != nil {
			return nil, fmt.Errorf("%s: window %d: %w", path, i+1, err)
		}
	}
	return t, nil
}

func (w Window) check() error {
	if _, err := clock(w.Start); err != nil {
		return err
	}
	if _, err := clock(w.End); err != nil {
		return err
	}
	if !(w.Multiplier >= 0 && w.Multiplier <= 1) {
		return fmt.Errorf("multiplier %v is not a fraction of the peak price", w.Multiplier)
	}
	if len(w.Models) == 0 {
		return errors.New("no models")
	}
	for _, s := range []string{w.From, w.Until} {
		if s != "" {
			if _, err := time.Parse(time.RFC3339, s); err != nil {
				return err
			}
		}
	}
	return nil
}

// clock parses "HH:MM" into minutes after midnight.
func clock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a UTC time of day (HH:MM)", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// matches reports whether the window prices model at instant at.
func (w Window) matches(model string, at time.Time) bool {
	m := strings.ToLower(model)
	hit := false
	for _, name := range w.Models {
		if name != "" && strings.Contains(m, strings.ToLower(name)) {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	if from, err := time.Parse(time.RFC3339, w.From); err == nil && at.Before(from) {
		return false
	}
	if until, err := time.Parse(time.RFC3339, w.Until); err == nil && !at.Before(until) {
		return false
	}
	start, err1 := clock(w.Start)
	end, err2 := clock(w.End)
	if err1 != nil || err2 != nil {
		return false
	}
	u := at.UTC()
	now := u.Hour()*60 + u.Minute()
	if start <= end {
		return now >= start && now < end
	}
	return now >= start || now < end
}

// Multiplier is the fraction of the peak price model costs at at: the
// cheapest window that applies, 1 when none does.
func (t Table) Multiplier(model string, at time.Time) float64 {
	best := 1.0
	for _, w := range t {
		if w.matches(model, at) && w.Multiplier < best {
			best = w.Multiplier
		}
	}
	return best
}

// Covers reports whether any window, current or expired, names model:
// "no discount for this model" and "this model's discount has ended" are
// different answers.
func (t Table) Covers(model string) bool {
	m := strings.ToLower(model)
	for _, w := range t {
		for _, name := range w.Models {
			if name != "" && strings.Contains(m, strings.ToLower(name)) {
				return true
			}
		}
	}
	return false
}

// Plan is when to start.
type Plan struct {
	Start time.Time
	// Multiplier is the average price fraction over the estimated run if
	// it starts at Start; Now is the same for starting immediately.
	Multiplier, Now float64
}

// Wait is how long until Start, from now.
func (p Plan) Wait(now time.Time) time.Duration { return max(p.Start.Sub(now), 0) }

// step is the resolution the plan works at: windows are set in minutes.
const step = time.Minute

// Schedule picks when to start a run of length est so that it finishes by
// deadline at the lowest average price, the earliest such start on a tie.
// A deadline that leaves no room — already past, or closer than est — is
// "start now": the deadline wins over the discount, always.
func (t Table) Schedule(model string, now, deadline time.Time, est time.Duration) Plan {
	now = now.Truncate(step)
	n := max(int(est/step), 1)
	// Every candidate start, a minute apart, up to the last one that still
	// finishes by the deadline; a deadline days away is capped at a week.
	starts := 0
	if latest := deadline.Add(-time.Duration(n) * step); latest.After(now) {
		starts = int(latest.Sub(now) / step)
	}
	starts = min(starts, 7*24*60)
	// The price of each minute once, then a sliding sum over the run's
	// length: the plan is the start whose run costs least on average.
	price := make([]float64, starts+n)
	for i := range price {
		price[i] = t.Multiplier(model, now.Add(time.Duration(i)*step))
	}
	sum := 0.0
	for _, m := range price[:n] {
		sum += m
	}
	p := Plan{Start: now, Now: sum / float64(n)}
	p.Multiplier = p.Now
	for i := 1; i <= starts; i++ {
		sum += price[i+n-1] - price[i-1]
		if m := sum / float64(n); m < p.Multiplier-1e-9 {
			p.Start, p.Multiplier = now.Add(time.Duration(i)*step), m
		}
	}
	return p
}
