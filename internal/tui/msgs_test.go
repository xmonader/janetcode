package tui

import (
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
)

func newTestQueue() *eventQueue {
	return &eventQueue{send: func(tea.Msg) {}}
}

func kinds(evs []agent.Event) []agent.EventKind {
	out := make([]agent.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

// TestQueueCoalescesAdjacentStreamText: a stream of text deltas must
// collapse to one entry, and the entry must say what the deltas said.
func TestQueueCoalescesAdjacentStreamText(t *testing.T) {
	q := newTestQueue()
	for _, s := range []string{"he", "ll", "o"} {
		q.push(agent.Event{Kind: agent.EvText, Text: s})
	}
	q.push(agent.Event{Kind: agent.EvThinking, Text: "hm"})
	q.push(agent.Event{Kind: agent.EvText, Text: "!"})
	evs := q.drain()
	if got, want := len(evs), 3; got != want {
		t.Fatalf("drained %d events, want %d: %v", got, want, kinds(evs))
	}
	if evs[0].Text != "hello" || evs[1].Text != "hm" || evs[2].Text != "!" {
		t.Fatalf("coalescing lost or reordered text: %q %q %q", evs[0].Text, evs[1].Text, evs[2].Text)
	}
}

// TestQueueKeepsToolInputPerCall: two calls streaming their input at the
// same time must not have their JSON merged into one blob.
func TestQueueKeepsToolInputPerCall(t *testing.T) {
	a, b := &agent.ToolCall{ID: "a"}, &agent.ToolCall{ID: "b"}
	q := newTestQueue()
	q.push(agent.Event{Kind: agent.EvToolInput, Call: a, Text: `{"x":`})
	q.push(agent.Event{Kind: agent.EvToolInput, Call: b, Text: `{"y":`})
	q.push(agent.Event{Kind: agent.EvToolInput, Call: a, Text: `1}`})
	evs := q.drain()
	if len(evs) != 3 {
		t.Fatalf("input for two calls was merged: %d entries", len(evs))
	}
	if evs[0].Call != a || evs[1].Call != b || evs[2].Call != a {
		t.Fatal("tool input was attributed to the wrong call")
	}
}

// TestQueueNeverDropsABoundary is the invariant the whole bound is built
// around: however much text a runaway model streams, every tool-call
// boundary still arrives, in order. Without it a tool card never closes.
func TestQueueNeverDropsABoundary(t *testing.T) {
	call := &agent.ToolCall{ID: "t1", Name: "bash"}
	boundaries := []agent.EventKind{
		agent.EvToolStart, agent.EvToolRun, agent.EvToolResult,
		agent.EvTextBlockDone, agent.EvVerdict, agent.EvTurnDone,
	}
	q := newTestQueue()
	flood := strings.Repeat("x", 64<<10)
	var want []agent.EventKind
	for _, k := range boundaries {
		// Far more text between each pair of boundaries than the queue
		// will hold, so the byte ceiling is hit over and over.
		for i := 0; i < 64; i++ {
			q.push(agent.Event{Kind: agent.EvText, Text: flood})
		}
		q.push(agent.Event{Kind: k, Call: call})
		want = append(want, k)
	}
	if q.bytes > queueMaxBytes {
		t.Fatalf("queue holds %d bytes, over the %d-byte bound", q.bytes, queueMaxBytes)
	}
	if len(q.events) > queueMaxEvents {
		t.Fatalf("queue holds %d events, over the %d bound", len(q.events), queueMaxEvents)
	}

	var got []agent.EventKind
	var sawNotice bool
	for _, e := range q.drain() {
		switch {
		case e.Kind == agent.EvNotice:
			sawNotice = true
		case !coalescable(e.Kind):
			got = append(got, e.Kind)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("boundaries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("boundaries = %v, want %v", got, want)
		}
	}
	if !sawNotice {
		t.Error("output was dropped without saying so")
	}
}

// TestQueueDropIsReportedOnce: the elision notice is emitted by the
// drain that follows the drop and not by the next one.
func TestQueueDropIsReportedOnce(t *testing.T) {
	q := newTestQueue()
	for i := 0; i < 4; i++ {
		q.push(agent.Event{Kind: agent.EvText, Text: strings.Repeat("y", queueMaxBytes/2)})
	}
	notices := 0
	for _, e := range q.drain() {
		if e.Kind == agent.EvNotice {
			notices++
			if !strings.Contains(e.Text, "dropped") {
				t.Errorf("notice does not say what happened: %q", e.Text)
			}
		}
	}
	if notices != 1 {
		t.Fatalf("got %d elision notices, want 1", notices)
	}
	q.push(agent.Event{Kind: agent.EvText, Text: "ok"})
	for _, e := range q.drain() {
		if e.Kind == agent.EvNotice {
			t.Fatal("the elision was reported a second time")
		}
	}
}

// TestQueueBoundsNonCoalescableFlood: a model that emits nothing but
// boundaries cannot be dropped, but it also must not be able to sit in
// the queue forever -- the entry cap is what keeps it honest once there
// is stream text to shed.
func TestQueueBoundsEntriesWithStreamText(t *testing.T) {
	q := newTestQueue()
	// Alternate so that no two text events coalesce, filling the queue
	// with entries rather than bytes.
	for i := 0; i < queueMaxEvents*2; i++ {
		if i%2 == 0 {
			q.push(agent.Event{Kind: agent.EvText, Text: "a"})
		} else {
			q.push(agent.Event{Kind: agent.EvThinking, Text: "b"})
		}
	}
	if len(q.events) > queueMaxEvents {
		t.Fatalf("queue holds %d events, over the %d bound", len(q.events), queueMaxEvents)
	}
	q.drain()
}

// TestQueueWakesOnceUntilDrained: push schedules exactly one flush for a
// burst, however long the burst is.
func TestQueueWakesOnceUntilDrained(t *testing.T) {
	var mu sync.Mutex
	var wakes int
	done := make(chan struct{}, 64)
	q := &eventQueue{send: func(tea.Msg) {
		mu.Lock()
		wakes++
		mu.Unlock()
		done <- struct{}{}
	}}
	for i := 0; i < 50; i++ {
		q.push(agent.Event{Kind: agent.EvText, Text: "x"})
	}
	<-done
	mu.Lock()
	n := wakes
	mu.Unlock()
	if n != 1 {
		t.Fatalf("a burst of 50 events woke the loop %d times, want 1", n)
	}
	q.drain()
	q.push(agent.Event{Kind: agent.EvText, Text: "x"})
	<-done
}

// TestQueueIsConcurrencySafe runs push against drain under -race the way
// the agent goroutine and the UI loop do.
func TestQueueIsConcurrencySafe(t *testing.T) {
	q := newTestQueue()
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				q.push(agent.Event{Kind: agent.EvText, Text: "x"})
				q.push(agent.Event{Kind: agent.EvToolResult, Call: &agent.ToolCall{ID: "c"}})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			q.drain()
		}
	}()
	wg.Wait()
	q.drain()
}
