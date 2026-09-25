package tui

import (
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

	key := median(21, func() {
		h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
		h.m.View()
	})
	grow := median(21, func() {
		h.m.transcript.info("one more line")
		h.m.refresh(false)
		h.m.View()
	})
	width := 100
	resize := median(3, func() {
		width++
		h.update(tea.WindowSizeMsg{Width: width, Height: 50})
		h.m.View()
	})
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	runtime.KeepAlive(h) // the session under measurement
	t.Logf("keystroke %v · new line %v · resize %v · heap %d MB", key, grow, resize, ms.HeapAlloc>>20)
	for _, c := range []struct {
		what        string
		got, budget time.Duration
	}{{"a keystroke", key, keyBudget}, {"a new transcript line", grow, growBudget}, {"a resize", resize, resizeBudget}} {
		if c.got > c.budget {
			t.Errorf("%s took %v on a 20,000-line session, over the %v budget", c.what, c.got, c.budget)
		}
	}
	if ms.HeapAlloc > heapBudget {
		t.Errorf("heap %d MB, over the %d MB budget", ms.HeapAlloc>>20, heapBudget>>20)
	}
}
