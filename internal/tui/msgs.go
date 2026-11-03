package tui

import (
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// memoryMsg reports what automatic extraction remembered.
type memoryMsg struct {
	added []memory.Entry
	err   error
	note  string
}

type flushMsg struct{}

type agentDoneMsg struct {
	err error
}

type tickMsg time.Time

// flashDoneMsg retires the finished-tool flash. One of these is in flight
// at a time, whatever the transcript is doing (see model.scheduleFlash).
type flashDoneMsg struct{}

type subagentDoneMsg struct {
	desc string
	err  error
}

type cronNoticeMsg string

type cronFireMsg struct {
	id, prompt string
}

// queueMaxBytes is how much streamed text the queue holds for a UI that
// has not drained yet. The loop drains on the next frame, so in practice
// a few kilobytes are in flight; this is the ceiling for the case that is
// not practice — a model streaming megabytes at a terminal that is not
// keeping up, which used to grow the buffer without any limit at all.
const queueMaxBytes = 1 << 20

// queueMaxEvents bounds the entry count. Coalescing keeps a stream well
// under it; what it really bounds is a flood of non-coalescable events.
const queueMaxEvents = 4096

// coalescable reports whether an event is a slice of a stream, so that
// two adjacent ones say the same thing as their concatenation. Only
// these may be merged, and only these may be dropped when the queue is
// full: every other kind is a boundary — a tool call starting, running
// or returning, a text block ending, a turn ending — and the transcript
// and the turn's bookkeeping are built from those. Lose one EvToolResult
// and a tool card never closes and the turn never settles.
func coalescable(k agent.EventKind) bool {
	switch k {
	case agent.EvText, agent.EvThinking, agent.EvToolInput:
		return true
	}
	return false
}

// eventQueue is the hand-off between the agent goroutine and the UI loop.
// It coalesces so that one slow render never throttles the model stream:
// the agent appends without blocking and the UI drains everything that
// accumulated on its next wake-up.
//
// Bound: at most queueMaxEvents entries holding at most queueMaxBytes of
// streamed text. It stays inside them by merging adjacent slices of the
// same stream, which is lossless; only once the byte ceiling is reached
// is anything dropped, and then it is the incoming streamed text alone —
// counted, and reported as an explicit elision by the next drain, never
// silently. Boundary events are always queued, in order, whatever the
// queue's state.
type eventQueue struct {
	mu        sync.Mutex
	events    []agent.Event
	bytes     int // streamed text currently queued
	dropped   int // streamed bytes refused since the last drain
	scheduled bool
	send      func(tea.Msg)
}

func (q *eventQueue) push(e agent.Event) {
	q.mu.Lock()
	q.appendLocked(e)
	wake := !q.scheduled
	q.scheduled = true
	q.mu.Unlock()
	if wake {
		go q.send(flushMsg{})
	}
}

// appendLocked adds one event, coalescing and enforcing the bound.
func (q *eventQueue) appendLocked(e agent.Event) {
	if !coalescable(e.Kind) {
		// A boundary. It is never dropped, so the entry cap can only be
		// made room for by shedding queued stream text.
		for len(q.events) >= queueMaxEvents && q.dropOldestStreamLocked() {
		}
		q.events = append(q.events, e)
		return
	}
	if q.bytes+len(e.Text) > queueMaxBytes {
		q.dropped += len(e.Text)
		return
	}
	if n := len(q.events); n > 0 {
		if last := &q.events[n-1]; last.Kind == e.Kind && last.Call == e.Call {
			// Two slices of one stream: their concatenation says exactly
			// what they did, and the entry count stays flat however long
			// the model writes for.
			last.Text += e.Text
			q.bytes += len(e.Text)
			return
		}
	}
	if len(q.events) >= queueMaxEvents && !q.dropOldestStreamLocked() {
		q.dropped += len(e.Text)
		return
	}
	q.bytes += len(e.Text)
	q.events = append(q.events, e)
}

// dropOldestStreamLocked removes the oldest coalescable entry to make
// room, and reports whether it found one. Boundaries are skipped.
func (q *eventQueue) dropOldestStreamLocked() bool {
	for i, e := range q.events {
		if !coalescable(e.Kind) {
			continue
		}
		q.dropped += len(e.Text)
		q.bytes -= len(e.Text)
		q.events = append(q.events[:i], q.events[i+1:]...)
		return true
	}
	return false
}

func (q *eventQueue) drain() []agent.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	evs := q.events
	if q.dropped > 0 {
		// Say so rather than letting the transcript quietly be short.
		evs = append(evs, agent.Event{
			Kind: agent.EvNotice,
			Text: fmt.Sprintf("%s of streamed output was dropped: the display could not keep up", textfmt.Bytes(int64(q.dropped))),
		})
		q.dropped = 0
	}
	q.events = nil
	q.bytes = 0
	q.scheduled = false
	return evs
}
