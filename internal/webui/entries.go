package webui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/present"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

type entry struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	// Tone colours a verdict: ok, warn or bad.
	Tone    present.Tone `json:"tone,omitempty"`
	Name    string       `json:"name,omitempty"`
	Summary string       `json:"summary,omitempty"`
	Output  string       `json:"output,omitempty"`
	Preview string       `json:"preview,omitempty"`
	Dur     string       `json:"dur,omitempty"`
	Error   bool         `json:"error,omitempty"`
	Denied  bool         `json:"denied,omitempty"`
	Done    bool         `json:"done,omitempty"`
	// Gone tells the page to remove the entry: the server dropped it
	// from the thread (an empty thinking placeholder, discarded text).
	Gone    bool   `json:"gone,omitempty"`
	Running bool   `json:"running,omitempty"`
	CallID  string `json:"call_id,omitempty"`
	// At is when the entry was created, for the hover timestamp.
	At time.Time `json:"at,omitempty"`
	// TokIn, TokOut and Ms are the turn's bill and wall time, stamped on
	// the assistant entry when the turn finishes: cost and duration live
	// where they happened, not only in the aggregate panel.
	TokIn  int   `json:"tok_in,omitempty"`
	TokOut int   `json:"tok_out,omitempty"`
	Ms     int64 `json:"ms,omitempty"`
	// Model is the model that wrote an assistant entry.
	Model string `json:"model,omitempty"`
	// Data is a structured payload for kinds the front-end renders as
	// views of their own (perf, usage): the same numbers Text would
	// carry, but drawable.
	Data any `json:"data,omitempty"`
	// Run fields fold two or more tools in one turn into a single row.
	// Run is the id of the first tool in the burst; RunHead is that row.
	// RunLive keeps the burst open while the turn is still going.
	Run      string `json:"run,omitempty"`
	RunHead  bool   `json:"run_head,omitempty"`
	RunLabel string `json:"run_label,omitempty"`
	RunLive  bool   `json:"run_live,omitempty"`
}

func (s *server) sheet(title, body string) {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		body = "(" + title + " is empty)"
	}
	s.add(entry{Kind: "sheet", Name: title, Text: body})
}

// card appends a structured entry: the payload rides as JSON and the
// front-end draws it, the way the trajectory panel does, rather than as
// a fixed-width text sheet.
func (s *server) card(kind string, data any) {
	s.add(entry{Kind: kind, Data: data})
}

// pageData is the payload of a "page" entry: which stored page the
// front-end frames. The sid pins the entry to the session that owns the
// page, so it still resolves after the page switches sessions.
type pageData struct {
	SID   string `json:"sid"`
	N     int    `json:"n"`
	Title string `json:"title"`
}

// addPage adds the card for a render_html result: the page itself, shown
// in a sandboxed frame by the front-end.
func (s *server) addPage(result string) {
	n, ok := tool.PageResultN(result)
	cur := s.sess()
	if !ok || cur == nil {
		return
	}
	s.closeOpen()
	s.add(entry{Kind: "page", Name: tool.PageResultTitle(result), Done: true,
		Data: pageData{SID: cur.Meta.ID, N: n, Title: tool.PageResultTitle(result)}})
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
		s.setPhaseIfChanged(present.PhaseWriting)
	case agent.EvThinking:
		s.appendText("thinking", ev.Text)
		s.setPhaseIfChanged(present.PhaseThinking)
	case agent.EvToolStart:
		s.closeOpen()
		s.add(entry{Kind: "tool", Name: ev.Call.Name, CallID: ev.Call.ID, Summary: present.ToolSummary(ev.Call.Name, string(ev.Call.Input), 80, s.cwd())})
		s.setPhaseIfChanged(present.PhasePreparing(ev.Call.Name))
	case agent.EvToolInput:
		// The status line says what is being written and how much, as the
		// TUI's does — a large write_file streams for a minute, and a
		// rotating filler word in the meantime tells the reader nothing.
		if ev.Call != nil {
			if p, ok := present.PhaseStreaming(ev.Call.Name, string(ev.Call.Input), s.cwd()); ok {
				s.setStreamingPhase(p)
			}
		}
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
			e.Summary = present.ToolSummary(ev.Call.Name, string(ev.Call.Input), 80, cwd)
		})
	case agent.EvToolRun:
		cwd := s.cwd()
		e := s.updateTool(ev.Call, func(e *entry) {
			e.Name = ev.Call.Name
			e.CallID = ev.Call.ID
			e.Running = true
			e.Summary = present.ToolSummary(ev.Call.Name, string(ev.Call.Input), 80, cwd)
			if ev.Call.Preview != "" {
				e.Preview = ev.Call.Preview
			}
		})
		s.setPhase(present.PhaseRunning(e.Name, string(ev.Call.Input), cwd))
	case agent.EvToolResult:
		if ev.Call != nil && ev.Call.Name == "goal_status" && !ev.IsError {
			// The terminal says when a /goal ends; the page said nothing
			// but a faint tool row.
			s.closeOpen()
			s.add(entry{Kind: "verdict", Text: "◎ " + present.GoalEnded(ev.Output), Tone: present.ToneWarn})
		}
		s.updateTool(ev.Call, func(e *entry) {
			if ev.Call != nil {
				e.Name, e.CallID = ev.Call.Name, ev.Call.ID
				if ev.Call.Preview != "" {
					e.Preview = ev.Call.Preview
				}
			}
			e.Running = false
			e.Done = true
			input := ""
			if ev.Call != nil {
				input = string(ev.Call.Input)
			}
			end := present.EndOf(e.Name, input, ev.Output, ev.IsError, ev.Duration)
			e.Output, e.Error, e.Dur = end.Output, end.Failed, end.Label
			e.Denied = ev.Denied
		})
		if ev.Call != nil && ev.Call.Name == "render_html" && !ev.IsError {
			s.addPage(ev.Output)
		}
		// The tool row already says it finished. Leaving the phase on the
		// tool made the status line keep spinning that command while the
		// model thought about the result.
		s.setPhase(present.PhaseThinking)
	case agent.EvTextBlockDone:
		s.closeOpen()
	case agent.EvTurnDone:
		s.closeOpen()
		// Stamp the answer with what the turn cost: the usage the agent
		// reports and the wall clock from begin(), so the bill shows
		// where it happened.
		if s.lastAssistant != "" {
			id := s.lastAssistant
			s.lastAssistant = ""
			u := ev.Usage
			ms := int64(time.Since(s.started) / time.Millisecond)
			if ms < 0 {
				ms = 0
			}
			s.update(id, func(en *entry) {
				en.TokIn = u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
				en.TokOut = u.OutputTokens
				en.Ms = ms
			})
		}
	case agent.EvDiscard:
		s.dropOpen()
	case agent.EvVerdict:
		s.closeOpen()
		s.add(entry{Kind: "verdict", Text: present.Verdict(s.cwd(), ev.Text), Tone: present.VerdictTone(ev.Text), Error: ev.IsError})
		// What the checks themselves say, on the same evidence model as
		// /status, the terminal and the headless result.
		if line, ok := present.Evidence(s.cwd(), ev.Evidence); ok {
			s.add(entry{Kind: "verdict", Text: line, Tone: present.VerdictTone(line), Error: ev.Evidence.Blocks})
		}
	case agent.EvRetry:
		s.closeOpen()
		s.add(entry{Kind: "notice", Text: "↻ " + ev.Text})
	case agent.EvNotice:
		if present.Routine(ev.Text) {
			return
		}
		s.closeOpen()
		s.add(entry{Kind: "notice", Text: present.Notice(s.cwd(), ev.Text)})
	case agent.EvPhase:
		s.setPhase(ev.Text)
	case agent.EvCompacted:
		s.closeOpen()
		s.add(entry{Kind: "notice", Text: "⇣ " + present.Compacted(ev.Text)})
	}
}

// setPhase records what the session is doing and tells the page.
func (s *server) setPhase(phase string) {
	s.mu.Lock()
	s.setPhaseLocked(phase)
	s.mu.Unlock()
	s.emitStatus()
}

// setPhaseIfChanged is setPhase for events that repeat per streamed
// chunk: the page hears only about a change.
func (s *server) setPhaseIfChanged(phase string) {
	s.mu.Lock()
	same := s.phase == phase
	s.setPhaseLocked(phase)
	s.mu.Unlock()
	if !same {
		s.emitStatus()
	}
}

// streamingPhaseEvery bounds how often a streaming progress phase ("main.go
// · writing 40 lines so far") is sent: every fragment changes it, and
// each status costs a token estimate and a broadcast.
const streamingPhaseEvery = 250 * time.Millisecond

func (s *server) setStreamingPhase(phase string) {
	s.mu.Lock()
	if s.phase == phase || time.Since(s.phaseAt) < streamingPhaseEvery {
		s.mu.Unlock()
		return
	}
	s.setPhaseLocked(phase)
	s.phaseAt = time.Now()
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
	if cp.Kind == "tool" || cp.Kind == "user" || cp.Kind == "assistant" {
		s.touchRuns()
	}
	return cp.ID
}

func (s *server) addLocked(e entry) *entry {
	s.nextID++
	e.ID = "e" + strconv.Itoa(s.nextID)
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Kind == "assistant" {
		s.lastAssistant = e.ID
		if e.Model == "" && s.opts.Agent != nil {
			e.Model = s.opts.Agent.Settings().Model
		}
	}
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
		if cp.Kind == "tool" {
			s.touchRuns()
		}
	}
}

// appendText adds streamed text to the open entry of this kind, starting
// one when there is none, and sends it.
func (s *server) appendText(kind, text string) {
	s.mu.Lock()
	var e *entry
	var gone []entry
	created := false
	if n := len(s.entries); n > 0 && s.entries[n-1].Kind == kind && !s.entries[n-1].Done {
		e = s.entries[n-1]
	} else if prev, noise := s.thoughtToContinueLocked(kind); prev != nil {
		// A thought after a thought, with nothing but a stray "." between,
		// is the same thought going on: one row, not a stack of them.
		for _, x := range noise {
			gone = append(gone, entry{ID: x.ID, Kind: x.Kind, Gone: true})
		}
		s.entries = s.entries[:len(s.entries)-len(noise)]
		e = prev
		e.Done = false
		e.Text = strings.TrimRight(e.Text, "\n") + "\n\n"
	} else {
		e = s.addLocked(entry{Kind: kind})
		created = true
	}
	e.Text += text
	cp := *e
	s.mu.Unlock()
	for _, g := range gone {
		s.emitEntry(g)
	}
	s.emitEntry(cp)
	if created && kind == "assistant" {
		s.touchRuns()
	}
}

// thoughtToContinueLocked is the finished thought a new thinking block
// continues, and the punctuation-only answers after it that would be
// dropped, when the thread ends in exactly that. Called with mu held.
func (s *server) thoughtToContinueLocked(kind string) (*entry, []*entry) {
	if kind != "thinking" {
		return nil, nil
	}
	var noise []*entry
	for i := len(s.entries) - 1; i >= 0; i-- {
		e := s.entries[i]
		switch {
		case e.Kind == "thinking" && strings.TrimSpace(e.Text) != "":
			return e, noise
		case e.Kind == "assistant" && e.Done && textfmt.PunctuationOnly(e.Text):
			noise = append(noise, e)
		default:
			return nil, nil
		}
	}
	return nil, nil
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
	s.touchRuns()
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
		// An empty thought, or an answer that is only punctuation (a
		// model's stray "." between tool calls), is not kept: the
		// terminal drops it at the same point, and the page used to keep
		// the row and hide it in JavaScript.
		if !e.Done && ((e.Kind == "thinking" && strings.TrimSpace(e.Text) == "") || (e.Kind == "assistant" && textfmt.PunctuationOnly(e.Text))) {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			closed = append(closed, entry{ID: e.ID, Kind: e.Kind, Gone: true})
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
	var gone []entry
	for len(s.entries) > 0 {
		e := s.entries[len(s.entries)-1]
		if (e.Kind == "assistant" || e.Kind == "thinking") && !e.Done {
			s.entries = s.entries[:len(s.entries)-1]
			gone = append(gone, entry{ID: e.ID, Kind: e.Kind, Gone: true})
			continue
		}
		break
	}
	s.mu.Unlock()
	for _, e := range gone {
		s.emitEntry(e)
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
