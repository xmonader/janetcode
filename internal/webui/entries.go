package webui

import (
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
	s.upsert(&entry{Kind: "sheet", Name: title, Text: body})
}

func (s *server) notice(text string) {
	s.emit(envelope{Type: "notice", Notice: text})
	s.upsert(&entry{Kind: "notice", Text: text})
}

func (s *server) errf(f string, a ...any) {
	s.upsert(&entry{Kind: "error", Text: fmt.Sprintf(f, a...), Error: true})
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
		e := s.last("assistant")
		if e == nil {
			e = s.add(&entry{Kind: "assistant"})
		}
		e.Text += ev.Text
		s.push(e)
	case agent.EvThinking:
		e := s.last("thinking")
		if e == nil {
			e = s.add(&entry{Kind: "thinking"})
		}
		e.Text += ev.Text
		s.push(e)
	case agent.EvToolStart:
		s.closeOpen()
		s.add(&entry{Kind: "tool", Name: ev.Call.Name, CallID: ev.Call.ID, Summary: summarizeInput(ev.Call.Name, string(ev.Call.Input), 80, s.cwd())})
	case agent.EvToolInput:
		if e := s.findTool(ev.Call); e != nil {
			e.Summary = summarizeInput(e.Name, e.Summary, 80, s.cwd())
		}
	case agent.EvToolRun:
		e := s.findTool(ev.Call)
		if e == nil {
			e = s.add(&entry{Kind: "tool", Name: ev.Call.Name, CallID: ev.Call.ID})
		}
		e.Name = ev.Call.Name
		e.CallID = ev.Call.ID
		e.Running = true
		e.Summary = summarizeInput(ev.Call.Name, string(ev.Call.Input), 80, s.cwd())
		if ev.Call.Preview != "" {
			e.Preview = ev.Call.Preview
		}
		s.push(e)
		s.mu.Lock()
		s.phase = e.Name + " " + e.Summary
		s.mu.Unlock()
		s.emitStatus()
	case agent.EvToolResult:
		e := s.findTool(ev.Call)
		if e == nil {
			e = s.add(&entry{Kind: "tool", Name: ev.Call.Name, CallID: ev.Call.ID})
		}
		e.Running = false
		e.Done = true
		e.Output = ev.Output
		e.Error = ev.IsError
		e.Denied = ev.Denied
		if ev.Duration > 0 {
			e.Dur = textfmt.Duration(ev.Duration)
		}
		if ev.Call != nil && ev.Call.Preview != "" {
			e.Preview = ev.Call.Preview
		}
		s.push(e)
	case agent.EvTextBlockDone:
		s.closeOpen()
	case agent.EvTurnDone:
		s.closeOpen()
	case agent.EvDiscard:
		s.dropOpen()
	case agent.EvVerdict:
		s.closeOpen()
		s.add(&entry{Kind: "verdict", Text: ev.Text, Error: ev.IsError})
	case agent.EvRetry:
		s.closeOpen()
		s.add(&entry{Kind: "notice", Text: "↻ " + ev.Text})
	case agent.EvNotice:
		s.closeOpen()
		s.add(&entry{Kind: "notice", Text: ev.Text})
	case agent.EvPhase:
		s.mu.Lock()
		s.phase = ev.Text
		s.mu.Unlock()
		s.emitStatus()
	case agent.EvCompacted:
		s.closeOpen()
		s.add(&entry{Kind: "notice", Text: "⇣ " + ev.Text})
	}
}

func (s *server) last(kind string) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.entries); n > 0 {
		if e := s.entries[n-1]; e.Kind == kind && !e.Done {
			return e
		}
	}
	return nil
}

func (s *server) add(e *entry) *entry {
	s.mu.Lock()
	s.nextID++
	e.ID = "e" + strconv.Itoa(s.nextID)
	s.entries = append(s.entries, e)
	s.mu.Unlock()
	s.push(e)
	return e
}

func (s *server) upsert(e *entry) *entry {
	if e.ID == "" {
		return s.add(e)
	}
	s.push(e)
	return e
}

func (s *server) push(e *entry) {
	cp := *e
	s.emit(envelope{Type: "entry", Entry: &cp})
}

func (s *server) findTool(call *agent.ToolCall) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if call == nil {
		for i := len(s.entries) - 1; i >= 0; i-- {
			if e := s.entries[i]; e.Kind == "tool" && !e.Done {
				return e
			}
		}
		return nil
	}
	for i := len(s.entries) - 1; i >= 0; i-- {
		if e := s.entries[i]; e.Kind == "tool" && e.CallID == call.ID {
			return e
		}
	}
	return nil
}

func (s *server) closeOpen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		if e.Kind == "thinking" && !e.Done && strings.TrimSpace(e.Text) == "" {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			continue
		}
		if e.Kind == "assistant" || e.Kind == "thinking" {
			if !e.Done {
				e.Done = true
				cp := *e
				s.mu.Unlock()
				s.push(&cp)
				s.mu.Lock()
			}
			continue
		}
		if e.Kind == "tool" {
			continue
		}
		break
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
