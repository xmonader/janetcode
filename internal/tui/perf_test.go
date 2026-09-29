package tui

import (
	"fmt"
	"runtime"
	"sort"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Performance budgets for a long session. Each is several times what it
// costs today, so a slow CI machine passes and a change that makes the
// interface do work proportional to the whole transcript on every frame
// fails here, not in someone's day-long session. Measured on a 20,000
// line transcript, about three days of steady use.
const (
	// A keystroke with nothing new in the transcript: typing must never
	// feel the session's length.
	keyBudget = 16 * time.Millisecond // one frame at 60 Hz
	// A new line in the transcript, which is every streamed frame of a
	// turn: the whole transcript is re-joined, but nothing re-rendered.
	// It was 39 ms before the scroller replaced bubbles' viewport.
	growBudget = 30 * time.Millisecond
	// A resize re-renders every entry at the new width, once.
	resizeBudget = 3 * time.Second
	// What the rendered transcript holds in memory.
	heapBudget = 200 << 20
	// budgetAttempts is how many times the timings are measured before a
	// miss counts.
	budgetAttempts = 3
)

// median runs f n times and returns the median duration.
func median(n int, f func()) time.Duration {
	var d []time.Duration
	for range n {
		start := time.Now()
		f()
		d = append(d, time.Since(start))
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[len(d)/2]
}

func TestPerfBudgetsOnALongSession(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("timing budgets are for a normal build")
	}
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 120, Height: 50})
	h.m.transcript = bigTranscript(20000)
	h.m.refresh(false)
	h.m.View()

	// A regression is over its budget every time it is measured; a
	// machine busy with other work (the full suite, other builds) is over
	// it now and then. So a miss is measured again, up to budgetAttempts
	// times, and only a budget missed on every attempt fails. The budgets
	// themselves stay as tight as before.
	width := 100
	var misses []string
	for attempt := 1; attempt <= budgetAttempts; attempt++ {
		key := median(21, func() {
			h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			h.m.View()
		})
		grow := median(21, func() {
			h.m.transcript.info("one more line")
			h.m.refresh(false)
			h.m.View()
		})
		resize := median(3, func() {
			width++
			h.update(tea.WindowSizeMsg{Width: width, Height: 50})
			h.m.View()
		})
		t.Logf("attempt %d: keystroke %v · new line %v · resize %v", attempt, key, grow, resize)
		misses = misses[:0]
		for _, c := range []struct {
			what        string
			got, budget time.Duration
		}{{"a keystroke", key, keyBudget}, {"a new transcript line", grow, growBudget}, {"a resize", resize, resizeBudget}} {
			if c.got > c.budget {
				misses = append(misses, fmt.Sprintf("%s took %v on a 20,000-line session, over the %v budget", c.what, c.got, c.budget))
			}
		}
		if len(misses) == 0 {
			break
		}
		time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
	}
	for _, m := range misses {
		t.Errorf("%s (on all %d attempts)", m, budgetAttempts)
	}
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	runtime.KeepAlive(h) // the session under measurement
	t.Logf("heap %d MB", ms.HeapAlloc>>20)
	if ms.HeapAlloc > heapBudget {
		t.Errorf("heap %d MB, over the %d MB budget", ms.HeapAlloc>>20, heapBudget>>20)
	}
}
