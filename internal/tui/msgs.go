package tui

import (
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/memory"
)

type agentEventMsg struct{ ev agent.Event }

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

type cronFireMsg struct {
	id, prompt string
}

// eventQueue coalesces agent events so that one slow render never throttles
// the model stream: the agent goroutine appends without blocking and the UI
// drains everything that accumulated on its next wake-up.
type eventQueue struct {
	mu        sync.Mutex
	events    []agent.Event
	scheduled bool
	send      func(tea.Msg)
}

func (q *eventQueue) push(e agent.Event) {
	q.mu.Lock()
	q.events = append(q.events, e)
	wake := !q.scheduled
	q.scheduled = true
	q.mu.Unlock()
	if wake {
		go q.send(flushMsg{})
	}
}

func (q *eventQueue) drain() []agent.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	evs := q.events
	q.events = nil
	q.scheduled = false
	return evs
}
