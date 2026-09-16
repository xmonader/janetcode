package webui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/textfmt"
)

type entry struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Text    string `json:"text,omitempty"`
	Name    string `json:"name,omitempty"`
	Summary string `json:"summary,omitempty"`
	Output  string `json:"output,omitempty"`
	Preview string `json:"preview,omitempty"`
	Dur     string `json:"dur,omitempty"`
	Error   bool   `json:"error,omitempty"`
	Denied  bool   `json:"denied,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Running bool   `json:"running,omitempty"`
	CallID  string `json:"call_id,omitempty"`
}

func (s *server) sheet(title, body string) {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		body = "(" + title + " is empty)"
	}
	s.add(entry{Kind: "sheet", Name: title, Text: body})
}

func (s *server) notice(text string) {
	s.emit(envelope{Type: "notice", Notice: text})
	s.add(entry{Kind: "notice", Text: text})
}

func (s *server) errf(f string, a ...any) {
	s.add(entry{Kind: "error", Text: fmt.Sprintf(f, a...), Error: true})
}

func (s *server) noteOnOff(name string, on bool, whenOn string) {
	if on {
		s.notice(name + " on · " + whenOn)
		return
	}
	s.notice(name + " off")
}

func (s *server) apply(ev agent.Event) {
	switch ev.Kind {
	case agent.EvText:
		s.appendText("assistant", ev.Text)
	case agent.EvThinking:
		s.appendText("thinking", ev.Text)
	case agent.EvToolStart:
		s.closeOpen()
		s.add(entry{Kind: "tool", Name: ev.Call.Name, CallID: ev.Call.ID, Summary: summarizeInput(ev.Call.Name, string(ev.Call.Input), 80, s.cwd())})
	case agent.EvToolInput:
		// Arguments stream in a fragment at a time. This used to
		// re-summarise the summary — which parses as nothing, so it left
		// the summary alone — and never sent the result either way. The
		// input is summarised now, but only once enough of it has arrived
		// to parse, so the page never shows half a JSON object.
		if ev.Call == nil || !json.Valid(ev.Call.Input) {
			break
		}
		cwd := s.cwd()
		s.updateTool(ev.Call, func(e *entry) {
			e.Summary = summarizeInput(ev.Call.Name, string(ev.Call.Input), 80, cwd)
		})
	case agent.EvToolRun:
		cwd := s.cwd()
		e := s.updateTool(ev.Call, func(e *entry) {
			e.Name = ev.Call.Name
			e.CallID = ev.Call.ID
			e.Running = true
			e.Summary = summarizeInput(ev.Call.Name, string(ev.Call.Input), 80, cwd)
			if ev.Call.Preview != "" {
				e.Preview = ev.Call.Preview
			}
		})
		s.setPhase(e.Name + " " + e.Summary)
	case agent.EvToolResult:
		s.updateTool(ev.Call, func(e *entry) {
			if ev.Call != nil {
				e.Name, e.CallID = ev.Call.Name, ev.Call.ID
				if ev.Call.Preview != "" {
					e.Preview = ev.Call.Preview
				}
			}
			e.Running = false
			e.Done = true
			e.Output = ev.Output
			e.Error = ev.IsError
			e.Denied = ev.Denied
			if ev.Duration > 0 {
				e.Dur = textfmt.Duration(ev.Duration)
			}
		})
	case agent.EvTextBlockDone:
		s.closeOpen()
	case agent.EvTurnDone:
		s.closeOpen()
	case agent.EvDiscard:
		s.dropOpen()
	case agent.EvVerdict:
		s.closeOpen()
		s.add(entry{Kind: "verdict", Text: ev.Text, Error: ev.IsError})
	case agent.EvRetry:
		s.closeOpen()
		s.add(entry{Kind: "notice", Text: "↻ " + ev.Text})
	case agent.EvNotice:
		s.closeOpen()
		s.add(entry{Kind: "notice", Text: ev.Text})
	case agent.EvPhase:
		s.setPhase(ev.Text)
	case agent.EvCompacted:
		s.closeOpen()
		s.add(entry{Kind: "notice", Text: "⇣ " + ev.Text})
	}
}

// setPhase records what the session is doing and tells the page.
func (s *server) setPhase(phase string) {
	s.mu.Lock()
	s.phase = phase
	s.mu.Unlock()
	s.emitStatus()
}

// The thread belongs to s.mu. An entry is created, changed and copied
// there, and everything outside the lock works on copies: entries used to
// be handed out as pointers and then mutated by the agent's goroutine
// while an HTTP request was serialising the very same struct.

// add appends a new entry, sends it, and returns its id.
func (s *server) add(e entry) string {
	s.mu.Lock()
	p := s.addLocked(e)
	cp := *p
	s.mu.Unlock()
	s.emitEntry(cp)
	return cp.ID
}

func (s *server) addLocked(e entry) *entry {
	s.nextID++
	e.ID = "e" + strconv.Itoa(s.nextID)
	p := &e
	s.entries = append(s.entries, p)
	return p
}

// update applies fn to the entry with this id and sends the result. An id
// that is no longer in the thread is ignored.
func (s *server) update(id string, fn func(*entry)) {
	s.mu.Lock()
	var cp entry
	found := false
	for _, e := range s.entries {
		if e.ID == id {
			fn(e)
			cp, found = *e, true
			break
		}
	}
	s.mu.Unlock()
	if found {
		s.emitEntry(cp)
	}
}

// appendText adds streamed text to the open entry of this kind, starting
// one when there is none, and sends it.
func (s *server) appendText(kind, text string) {
	s.mu.Lock()
	var e *entry
	if n := len(s.entries); n > 0 && s.entries[n-1].Kind == kind && !s.entries[n-1].Done {
		e = s.entries[n-1]
	} else {
		e = s.addLocked(entry{Kind: kind})
	}
	e.Text += text
	cp := *e
	s.mu.Unlock()
	s.emitEntry(cp)
}

// updateTool applies fn to the entry for this call, starting one when the
// call has not been seen, and sends it. It returns the entry as it now
// stands, so a caller can use it without reaching back into the thread.
func (s *server) updateTool(call *agent.ToolCall, fn func(*entry)) entry {
	s.mu.Lock()
	e := s.findToolLocked(call)
	if e == nil {
		t := entry{Kind: "tool"}
		if call != nil {
			t.Name, t.CallID = call.Name, call.ID
		}
		e = s.addLocked(t)
	}
	fn(e)
	cp := *e
	s.mu.Unlock()
	s.emitEntry(cp)
	return cp
}

func (s *server) emitEntry(e entry) {
	s.emit(envelope{Type: "entry", Entry: &e})
}

// findToolLocked is the entry for a call, or the last tool still running
// when the call is unknown. Called with mu held.
func (s *server) findToolLocked(call *agent.ToolCall) *entry {
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if e.Kind != "tool" {
			continue
		}
		if call == nil {
			if !e.Done {
				return e
			}
			continue
		}
		if e.CallID == call.ID {
			return e
		}
	}
	return nil
}

func (s *server) closeOpen() {
	s.mu.Lock()
	var closed []entry
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if e.Kind == "thinking" && !e.Done && strings.TrimSpace(e.Text) == "" {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			continue
		}
		if e.Kind == "assistant" || e.Kind == "thinking" {
			if !e.Done {
				e.Done = true
				closed = append(closed, *e)
			}
			continue
		}
		if e.Kind == "tool" {
			continue
		}
		break
	}
	s.mu.Unlock()
	// Sent after the lock, oldest first: the loop walks backwards, and
	// releasing the lock inside it used to let the thread change under
	// the index it was walking.
	for i := len(closed) - 1; i >= 0; i-- {
		s.emitEntry(closed[i])
	}
}

func (s *server) dropOpen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.entries) > 0 {
		e := s.entries[len(s.entries)-1]
		if (e.Kind == "assistant" || e.Kind == "thinking") && !e.Done {
			s.entries = s.entries[:len(s.entries)-1]
			continue
		}
		return
	}
}

// showResult renders what a core command function returned: err as an
// error entry, otherwise the Result's sheet, error, notes and tips as
// entries and its status on the status line. A Result's Handover is left
// to the caller, which decides when to start it.
func (s *server) showResult(res cmdresult.Result, err error) {
	if err != nil {
		s.errf("%v", err)
		return
	}
	if res.Sheet != "" {
		s.sheet(res.SheetTitle, res.Sheet)
	}
	if res.Error != "" {
		s.errf("%s", res.Error)
	}
	for _, n := range res.Notes {
		s.notice(n)
	}
	for _, t := range res.Tips {
		s.notice(t)
	}
	if res.Status != "" {
		s.statusSet(res.Status)
	}
}
