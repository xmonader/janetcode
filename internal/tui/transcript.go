package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

type entryKind int

const (
	entryInfo entryKind = iota
	entryCode           // verbatim lines: file views and diffs, coloured by prefix
	entryError
	entryUser
	entryAssistant
	entryThinking
	entryTool
	entryBanner
	entryVerdict // what the turn demonstrated, with its mark
	entryTip     // a one-time hint about a feature, fainter than the rest
)

// The glyph on an information line is a small fixed vocabulary. A line
// that answers something you just typed carries none; a line that arrives
// on its own says in one glyph where it came from:
//
//	»  what the harness added to this request: skills, recalled memory,
//	   the repository map
//	⚑  something the harness noticed and you should know about
//	◆  the memory store changed
//	⚙  a background job
//	⇣  the context was compacted
//	↶  an undo or redo moved the tree or the conversation
//	◎  the session goal
//
// Two marks outside this vocabulary keep their own meaning: the ◆ in the
// header is the application's own mark, and the run of marks at the head
// of a tool line (⚙ queued, ▶ or the spinner running, ✓ done, ✗ failed,
// ⊘ denied) reports that call's state.
//
// Tips are the exception that carries no glyph: they are one-time hints
// about a feature, the least urgent line on the screen, and are rendered
// fainter than everything else.
const (
	noteAttached = "» "
	noteNotice   = "⚑ "
	noteMemory   = "◆ "
	noteJob      = "⚙ "
	noteCompact  = "⇣ "
	noteUndo     = "↶ "
	noteGoal     = "◎ "
	noteRetry    = "↻ "
)

type entry struct {
	kind entryKind
	text string // user text, assistant markdown, thinking, info/error text

	// tool fields
	call    agent.ToolCall
	input   strings.Builder // streamed partial JSON
	output  string
	isError bool
	done    bool
	running bool
	denied  bool
	dur     string
	started time.Time
	// ended is when the call finished, for the brief flash that says so.
	ended time.Time

	// expanded shows this one entry in full, whatever the global
	// /verbose flag says. ctrl+o sets it on the last turn's entries and a
	// mouse click on a tool line sets it on that entry alone.
	expanded bool
	// livePreview is the diff computed while the call was still being
	// written, kept against the input it came from so it is computed once.
	livePreview, livePreviewFor string
	// lineFrom and lineTo are the entry's line range in the last rendered
	// transcript, so a mouse click can be traced back to it.
	lineFrom, lineTo int

	// render cache
	rendered   string
	width      int
	final      bool // rendered from final content, safe to reuse
	renderedAt time.Time
	renderedN  int           // text length when rendered
	renderCost time.Duration // how long the last render took, for throttling
	verbose    bool          // rendered with expanded tool output
	chunks     []mdChunk     // per-paragraph markdown cache
	// toolBody is the tool entry without the leading icon and without the
	// trailing elapsed on the first line. Spinner and elapsed are chrome:
	// they are applied each frame without rebuilding the body.
	toolBody  string
	toolStamp string // last content fingerprint; chrome is not in it
}

// mdChunk caches the rendering of one markdown block.
type mdChunk struct {
	src, out string
	width    int
	// expanded is whether the block was rendered for a reader who asked
	// to see it in full: a mermaid diagram is drawn when it is folded and
	// shown as its source when it is expanded, so the cache has to know
	// which of the two it holds.
	expanded bool
	// partial marks a block whose fence was still open when it was
	// rendered. The text is the same once the fence closes — the renderer
	// closes it itself — so without this the finished block would be
	// served from the cache and never drawn.
	partial bool
}

// transcript is the ordered list of things shown in the viewport.
type transcript struct {
	entries   []*entry
	renderer  *glamour.TermRenderer
	rWidth    int
	styleName string // glamour standard style: "dark" or "light"
	styles    *styles
	// cwd shortens the absolute paths the agent writes into notices and
	// verdicts, the way tool lines already shorten theirs.
	cwd string
	// lastRender is the size of the frame just built, so the next one can
	// size its builder in one allocation instead of growing into it.
	lastRender int
}

// liveRenderInterval throttles markdown rendering of streaming text.
const liveRenderInterval = 80 * time.Millisecond

func newTranscriptStyled(style string, sty *styles) *transcript {
	return &transcript{styleName: style, styles: sty}
}

// restyle switches the transcript to a new display mode ("dark"/"light")
// and styles, and drops every cached render so the next frame redraws every
// entry with the palette the theme just built. It keeps the entries themselves,
// so a live /theme switch recolours the whole conversation in place with no
// restart. Cheap: rendering is per-entry and the tail is what usually
// re-renders anyway.
func (t *transcript) restyle(style string, sty *styles) {
	t.styleName = style
	t.styles = sty
	t.renderer = nil
	t.rWidth = 0
	for _, e := range t.entries {
		e.rendered = ""
		e.width = 0
		e.final = false
	}
	t.lastRender = 0
}

func (t *transcript) add(e *entry) *entry {
	t.entries = append(t.entries, e)
	return e
}

func (t *transcript) info(s string)   { t.add(&entry{kind: entryInfo, text: s}) }
func (t *transcript) tip(s string)    { t.add(&entry{kind: entryTip, text: s}) }
func (t *transcript) code(s string)   { t.add(&entry{kind: entryCode, text: s}) }
func (t *transcript) banner(s string) { t.add(&entry{kind: entryBanner, text: s}) }
func (t *transcript) user(s string)   { t.add(&entry{kind: entryUser, text: s}) }

func (t *transcript) errorf(f string, a ...any) {
	t.add(&entry{kind: entryError, text: fmt.Sprintf(f, a...)})
}

// last returns the trailing entry if it has the given kind and is still open.
func (t *transcript) last(kind entryKind) *entry {
	if n := len(t.entries); n > 0 {
		if e := t.entries[n-1]; e.kind == kind && !e.done {
			return e
		}
	}
	return nil
}

func (t *transcript) apply(ev agent.Event) {
	switch ev.Kind {
	case agent.EvText:
		e := t.last(entryAssistant)
		if e == nil {
			e = t.add(&entry{kind: entryAssistant})
		}
		e.text += ev.Text
	case agent.EvThinking:
		e := t.last(entryThinking)
		if e == nil {
			e = t.add(&entry{kind: entryThinking, started: time.Now()})
		}
		e.text += ev.Text
	case agent.EvToolStart:
		t.closeOpen()
		t.add(&entry{kind: entryTool, call: *ev.Call})
	case agent.EvToolInput:
		if e := t.lastTool(ev.Call); e != nil {
			e.input.WriteString(ev.Text)
		}
	case agent.EvToolRun:
		e := t.findTool(ev.Call.ID)
		if e == nil {
			t.closeOpen()
			e = t.add(&entry{kind: entryTool})
		}
		e.call = *ev.Call
		e.running = true
		e.started = time.Now()
	case agent.EvToolResult:
		if ev.Call != nil && ev.Call.Name == "goal_status" && !ev.IsError {
			// The goal's end is the one line the user is waiting for; a
			// tool result in faint text is not where they look for it.
			t.closeOpen()
			t.add(&entry{kind: entryVerdict, text: noteGoal + strings.TrimSpace(ev.Output) + " · nothing more runs for it; /goal starts another"})
		}
		e := t.findTool(ev.Call.ID)
		if e == nil {
			e = t.add(&entry{kind: entryTool, call: *ev.Call})
		}
		e.call = *ev.Call
		e.running = false
		e.done = true
		e.ended = time.Now()
		e.output = ev.Output
		e.isError = ev.IsError
		if ev.Duration > 0 {
			e.dur = textfmt.Duration(ev.Duration)
		}
	case agent.EvDiscard:
		t.dropOpen()
	case agent.EvTextBlockDone:
		t.closeOpen()
	case agent.EvTurnDone:
		t.closeOpen()
	case agent.EvCompacted:
		t.closeOpen()
		t.info(noteCompact + ev.Text + " · expand_output brings archived output back; /context shows what remains")
	case agent.EvVerdict:
		// The one line that says whether the turn showed its work. It was
		// dropped here for a long time while headless mode printed it.
		t.closeOpen()
		t.add(&entry{kind: entryVerdict, text: shortenPaths(t.cwd, ev.Text), isError: ev.IsError})
	case agent.EvRetry:
		// The status line that counted the retry down is gone the moment
		// the call lands, so a turn that cost four attempts read exactly
		// like one that cost none. One dim line under the turn says so.
		t.closeOpen()
		t.info(noteRetry + ev.Text)
	case agent.EvNotice:
		t.closeOpen()
		if isAttachNote(ev.Text) {
			t.info(noteAttached + shortenPaths(t.cwd, ev.Text))
		} else {
			t.info(noteNotice + shortenPaths(t.cwd, ev.Text))
		}
	}
}

func (t *transcript) lastTool(call *agent.ToolCall) *entry {
	if call == nil {
		for i := len(t.entries) - 1; i >= 0; i-- {
			if e := t.entries[i]; e.kind == entryTool && !e.done {
				return e
			}
		}
		return nil
	}
	return t.findTool(call.ID)
}

func (t *transcript) findTool(id string) *entry {
	for i := len(t.entries) - 1; i >= 0; i-- {
		if e := t.entries[i]; e.kind == entryTool && e.call.ID == id {
			return e
		}
	}
	return nil
}

// dropOpen throws away the text and thinking still streaming: the call
// that produced them failed and is being retried elsewhere, so half an
// answer followed by a whole one would read as two.
func (t *transcript) dropOpen() {
	for len(t.entries) > 0 {
		e := t.entries[len(t.entries)-1]
		if (e.kind == entryAssistant || e.kind == entryThinking) && !e.done {
			t.entries = t.entries[:len(t.entries)-1]
			continue
		}
		return
	}
}

// closeOpen marks streaming text/thinking entries finished so the next delta
// starts a new entry and the finished one gets rendered as final markdown.
func (t *transcript) closeOpen() {
	for i := len(t.entries) - 1; i >= 0; i-- {
		e := t.entries[i]
		if e.kind == entryThinking && !e.done && strings.TrimSpace(e.text) == "" {
			t.entries = append(t.entries[:i], t.entries[i+1:]...)
			continue
		}
		if e.kind == entryAssistant || e.kind == entryThinking {
			if !e.done {
				e.done = true
				e.final = false
				e.ended = time.Now()
			}
			continue
		}
		if e.kind == entryTool {
			continue
		}
		break
	}
}

func (t *transcript) finishTurn() { t.closeOpen() }

// dropEmptyTrailing removes a streaming assistant entry that never got text
// (e.g. a cancelled turn) so no blank block lingers.
func (t *transcript) dropEmptyTrailing() {
	if n := len(t.entries); n > 0 {
		if e := t.entries[n-1]; e.kind == entryAssistant && strings.TrimSpace(e.text) == "" {
			t.entries = t.entries[:n-1]
		}
	}
}

func (t *transcript) permissionAnswered(call agent.ToolCall, d agent.Decision) {
	if e := t.findTool(call.ID); e != nil && d == agent.Deny {
		e.denied = true
	}
}

// loadHistory renders a resumed conversation.
func (t *transcript) loadHistory(msgs []llm.Message) {
	results := map[string]llm.Block{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				results[b.ToolUseID] = b
			}
		}
	}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			switch {
			case m.Role == llm.RoleUser && b.Type == llm.BlockText:
				t.user(b.Text)
			case b.Type == llm.BlockText:
				t.add(&entry{kind: entryAssistant, text: b.Text, done: true})
			case b.Type == llm.BlockToolUse:
				e := &entry{kind: entryTool, call: agent.ToolCall{ID: b.ID, Name: b.Name, Input: b.Input}, done: true}
				e.input.Write(b.Input)
				r, ok := results[b.ID]
				if !ok {
					// Only terminal states are replayed. A call with no
					// result never reached one — the session ended first
					// — and must not come back wearing a tick.
					e.isError, e.output = true, "no result recorded: the session ended before this call returned"
					t.add(e)
					continue
				}
				e.output, e.isError = r.Text, r.IsError
				t.add(e)
			}
		}
	}
}

// --- rendering ---

// abortUnfinished closes tool entries that never got a result, so a turn
// that ended in a provider error does not leave a spinner turning against
// a call that will never come back. It reports how many it closed.
func (t *transcript) abortUnfinished(reason string) int {
	n := 0
	for _, e := range t.entries {
		if e.kind == entryTool && !e.done {
			e.running, e.done, e.isError, e.final = false, true, true, false
			if strings.TrimSpace(e.output) == "" {
				e.output = reason
			}
			n++
		}
	}
	return n
}

// toggleLastTurn expands the entries of the most recent turn, or collapses
// them when they are already expanded. Only the last turn: reopening the
// whole transcript is what /verbose is for, and kimi's rule — recent turns
// only — keeps the jump on screen. It reports what it did.
func (t *transcript) toggleLastTurn() (expanded, any bool) {
	start := 0
	for i := len(t.entries) - 1; i >= 0; i-- {
		if t.entries[i].kind == entryUser {
			start = i
			break
		}
	}
	all := true
	for _, e := range t.entries[start:] {
		if e.expandable() {
			any = true
			if !e.expanded {
				all = false
			}
		}
	}
	if !any {
		return false, false
	}
	for _, e := range t.entries[start:] {
		if e.expandable() {
			e.expanded = !all
			e.final = false
		}
	}
	return !all, true
}

// entryAtLine finds the expandable entry drawn on a line of the rendered
// transcript, for a mouse click.
func (t *transcript) entryAtLine(line int) *entry {
	for _, e := range t.entries {
		if line >= e.lineFrom && line <= e.lineTo && e.expandable() {
			return e
		}
	}
	return nil
}

// previousResult is the last result of the same tool before e, or "".
func (t *transcript) previousResult(e *entry) string {
	for i := len(t.entries) - 1; i >= 0; i-- {
		if t.entries[i] != e {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			p := t.entries[j]
			if p.kind == entryTool && p.call.Name == e.call.Name && p.done && !p.isError {
				return strings.TrimRight(p.output, "\n")
			}
		}
		return ""
	}
	return ""
}

// expandable reports whether showing this entry in full would say more
// than the folded form does.
func (e *entry) expandable() bool {
	switch e.kind {
	case entryTool:
		return e.output != "" || e.call.Preview != "" || e.input.Len() > 0
	case entryThinking:
		return e.done && strings.TrimSpace(e.text) != ""
	case entryAssistant:
		// A message is expandable when something in it was drawn rather
		// than shown: ctrl+o swaps the diagram for its mermaid source.
		return e.done && strings.Contains(e.text, "```mermaid")
	}
	return false
}

// thoughtFor is how long the model spent on a finished thought, or zero
// when the entry did not record it (a resumed conversation).
func (e *entry) thoughtFor() time.Duration {
	if e.started.IsZero() || e.ended.IsZero() || !e.ended.After(e.started) {
		return 0
	}
	return e.ended.Sub(e.started)
}

func (e *entry) inputJSON() string {
	if e.input.Len() > 0 {
		return e.input.String()
	}
	return string(e.call.Input)
}
