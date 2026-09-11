package tui

import (
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
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
}

// mdChunk caches the rendering of one markdown block.
type mdChunk struct {
	src, out string
	width    int
}

// renderOpts carries per-frame state into rendering.
type renderOpts struct {
	width int
	// gutter is the left margin every top-level line steps in from, so the
	// existing 2-column body indent steps in consistently from it. The
	// effective content width is width-gutter; entries are rendered to that
	// and then indented, so the body indent and diffs stay relative to the
	// gutter, not the terminal edge.
	gutter  int
	resized bool
	pending *permissionMsg
	spinner string // current spinner frame for running tools
	verbose bool   // expand tool output
	cwd     string // paths in tool lines are shown relative to it
	// cap is how many lines of a tool result are shown before folding. It
	// comes from the terminal's height: a fixed eight lines is most of a
	// short window and a waste of a tall one.
	cap int
	// tight drops what is optional in a very short window (the welcome
	// banner); compact narrows what is left.
	tight, compact bool
	// preview computes what a call would do, for a call whose arguments
	// have finished streaming but which has not run yet.
	preview func(name string, input []byte) string
	// renderer finds a tool that knows how to present itself.
	renderer func(name string) (tool.Renderer, bool)
}

// outputCap is the fold height for a viewport: a third of it, never fewer
// than four lines and never more than forty.
func outputCap(viewport int) int {
	c := viewport / 3
	if c < 4 {
		c = 4
	}
	if c > 40 {
		c = 40
	}
	return c
}

// lines is the fold height for this frame, or the given default when the
// terminal height is not known (tests, headless rendering).
func (o renderOpts) lines(def int) int {
	if o.cap > 0 {
		return o.cap
	}
	return def
}

// expand reports whether this entry is shown in full: either the session
// is verbose or the entry was expanded on its own.
func (o renderOpts) expand(e *entry) bool { return o.verbose || e.expanded }

// transcript is the ordered list of things shown in the viewport.
type transcript struct {
	entries   []*entry
	renderer  *glamour.TermRenderer
	rWidth    int
	styleName string // glamour standard style: "dark" or "light"
	// cwd shortens the absolute paths the agent writes into notices and
	// verdicts, the way tool lines already shorten theirs.
	cwd string
	// lastRender is the size of the frame just built, so the next one can
	// size its builder in one allocation instead of growing into it.
	lastRender int
}

// liveRenderInterval throttles markdown rendering of streaming text.
const liveRenderInterval = 80 * time.Millisecond

func newTranscript() *transcript { return newTranscriptStyled("dark") }

func newTranscriptStyled(style string) *transcript { return &transcript{styleName: style} }

// restyle switches the transcript to a new display mode ("dark"/"light")
// and drops every cached render so the next frame redraws every entry with
// the palette the theme just installed. It keeps the entries themselves,
// so a live /theme switch recolours the whole conversation in place with no
// restart. Cheap: rendering is per-entry and the tail is what usually
// re-renders anyway.
func (t *transcript) restyle(style string) {
	t.styleName = style
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
			e.dur = shortDuration(ev.Duration)
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

func (t *transcript) render(o renderOpts) string {
	width := o.width
	if width < 20 {
		width = 20
	}
	// The gutter narrows the content and indents it: everything an entry
	// renders is sized to width-gutter, so its own inner indents and diff
	// gutters stay relative to the margin rather than overrunning it. A
	// gutter that would leave less than the 20-column floor is dropped.
	g := o.gutter
	if g < 0 {
		g = 0
	}
	if g > 0 && width-g >= 20 {
		width -= g
	} else {
		g = 0
	}
	// From here on the entries are rendered at the narrowed width; o.width
	// carries it so needsRender's width comparison and renderEntry agree.
	o.width = width
	if t.renderer == nil || t.rWidth != width {
		r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle(t.styleName == "dark")), glamour.WithWordWrap(width-2))
		if err == nil {
			t.renderer = r
			t.rWidth = width
		}
	}
	now := time.Now()
	var sb strings.Builder
	// The whole transcript is concatenated every frame, so on a long one
	// the builder's doubling is most of what the render allocates: five
	// copies of a megabyte to produce one. The last frame's size is the
	// right guess for this one.
	sb.Grow(t.lastRender + 1024)
	line := 0
	for i, e := range t.entries {
		if e.kind == entryBanner && o.tight {
			// Nothing optional survives in a window this short.
			e.lineFrom, e.lineTo = -1, -1
			continue
		}
		if i > 0 {
			sb.WriteByte('\n')
			line++
		}
		if t.needsRender(e, o, now) {
			start := time.Now()
			e.rendered = t.renderEntry(e, width, o)
			e.renderCost = time.Since(start)
			e.width = width
			e.final = e.done
			e.renderedAt = now
			e.renderedN = len(e.text)
			e.verbose = o.expand(e)
		}
		// The gutter is applied on the way out, not baked into the cached
		// render, so it costs nothing when it collapses to zero and the
		// per-entry cache survives a width-preserving toggle. Indenting adds
		// no newlines, so the line map is unchanged.
		out := e.rendered
		if g > 0 {
			out = indent(out, strings.Repeat(" ", g))
		}
		e.lineFrom = line
		line += strings.Count(out, "\n")
		e.lineTo = line
		sb.WriteString(out)
		sb.WriteByte('\n')
		line++
	}
	t.lastRender = sb.Len()
	return sb.String()
}

// fixedText reports whether an entry's content can never change after it
// is added. Only assistant and thinking text grows (transcript.apply
// appends to it) and only a tool entry has a spinner, a duration and an
// output that arrive later; everything else is written once. Without this
// every notice, every line the user typed and the banner were re-rendered
// on every frame for the life of the session, because they are never
// marked done and the switch below fell through to "yes".
func fixedText(k entryKind) bool {
	switch k {
	case entryInfo, entryCode, entryError, entryUser, entryBanner, entryVerdict, entryTip:
		return true
	}
	return false
}

func (t *transcript) needsRender(e *entry, o renderOpts, now time.Time) bool {
	if e.rendered == "" || e.width != o.width || e.verbose != o.expand(e) {
		return true
	}
	if fixedText(e.kind) {
		return false
	}
	if e.done && e.final {
		return e.kind == entryTool && o.pending != nil && o.pending.call.ID == e.call.ID
	}
	switch e.kind {
	case entryTool:
		return true // running: spinner/elapsed changes each frame
	case entryAssistant, entryThinking:
		if e.done {
			return true
		}
		// Streaming: re-render when text grew and the throttle allows. The
		// interval stretches with the cost of the last render so a huge
		// message never monopolises the UI loop.
		interval := liveRenderInterval
		if c := 4 * e.renderCost; c > interval {
			interval = c
		}
		if interval > time.Second {
			interval = time.Second
		}
		return len(e.text) != e.renderedN && now.Sub(e.renderedAt) >= interval
	}
	return true
}

func (t *transcript) renderEntry(e *entry, width int, o renderOpts) string {
	// wrap folds text to the width and drops the padding lipgloss adds
	// to the right edge: padding spaces carry the style, and a terminal
	// that draws faint or italic as reverse video turns them into a bar.
	wrap := func(s string) string { return unpad(lipgloss.NewStyle().Width(width - 2).Render(s)) }
	switch e.kind {
	case entryBanner:
		return styleDim.Render(wrap(e.text))
	case entryInfo:
		return styleDim.Render(wrap(e.text))
	case entryTip:
		return styleFaint.Render(wrap("tip · " + e.text))
	case entryCode:
		return renderCode(e.text, width)
	case entryError:
		return styleErr.Render(errorBlock(e.text, width))
	case entryVerdict:
		style := styleWarn
		switch {
		case strings.HasPrefix(e.text, "✓"):
			style = styleOK
		case strings.HasPrefix(e.text, "✗"):
			style = styleErr
		}
		return style.Render(wrap(e.text))
	case entryUser:
		body := unpad(lipgloss.NewStyle().Width(width - 4).Render(e.text))
		return styleUser.Render("❯ ") + styleUserBody.Render(strings.ReplaceAll(body, "\n", "\n  "))
	case entryThinking:
		// Faint, never italic: many terminals have no italic face and
		// draw SGR italic as reverse video.
		if o.expand(e) && e.done {
			return styleFaint.Render(wrap("· thought: " + strings.TrimSpace(e.text)))
		}
		return styleFaint.Render(wrap(thinkingView(e.text, e.done, e.thoughtFor())))
	case entryAssistant:
		return t.renderMarkdown(e, width)
	case entryTool:
		return t.renderTool(e, width, o)
	}
	return e.text
}

// renderMarkdown renders through glamour block by block, caching every
// block whose source has not changed. While streaming only the last block
// is re-rendered, which keeps the cost proportional to the tail rather than
// the whole message.
func (t *transcript) renderMarkdown(e *entry, width int) string {
	text := e.text
	if strings.TrimSpace(text) == "" {
		if e.done {
			return ""
		}
		return styleFaint.Render("▍")
	}
	if t.renderer == nil {
		return lipgloss.NewStyle().Width(width - 2).Render(text)
	}
	blocks := splitMarkdownBlocks(text)
	if len(e.chunks) > len(blocks) {
		e.chunks = e.chunks[:len(blocks)]
	}
	var sb strings.Builder
	for i, src := range blocks {
		last := i == len(blocks)-1
		if last && !e.done && strings.Count(src, "```")%2 == 1 {
			// Close an unterminated fence so the partial code block still
			// gets highlighted instead of swallowing the rest.
			src += "\n```"
		}
		if i < len(e.chunks) && e.chunks[i].src == src && e.chunks[i].width == width {
			sb.WriteString(e.chunks[i].out)
			continue
		}
		out, err := t.renderer.Render(src)
		if err != nil {
			out = lipgloss.NewStyle().Width(width - 2).Render(src)
		}
		out = strings.Trim(out, "\n") + "\n"
		if i < len(e.chunks) {
			e.chunks[i] = mdChunk{src, out, width}
		} else {
			e.chunks = append(e.chunks, mdChunk{src, out, width})
		}
		sb.WriteString(out)
	}
	out := strings.TrimRight(sb.String(), "\n")
	if !e.done {
		out += styleFaint.Render(" ▍")
	}
	return out
}

// splitMarkdownBlocks splits markdown at blank lines that are outside fenced
// code blocks, so each piece renders independently.
func splitMarkdownBlocks(text string) []string {
	var blocks []string
	var cur strings.Builder
	inFence := false
	flush := func() {
		if s := strings.TrimRight(cur.String(), "\n"); strings.TrimSpace(s) != "" {
			blocks = append(blocks, s)
		}
		cur.Reset()
	}
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if trimmed == "" && !inFence {
			flush()
			continue
		}
		cur.WriteString(line)
	}
	flush()
	return blocks
}

func (t *transcript) renderTool(e *entry, width int, o renderOpts) string {
	var sb strings.Builder
	icon := styleTool.Render("⚙")
	switch {
	case e.denied:
		icon = styleErr.Render("⊘")
	case e.done && e.isError:
		icon = styleErr.Render("✗")
	case e.done && !e.ended.IsZero() && time.Since(e.ended) < flashFor:
		// A call that has just finished flashes for a moment, so a glance
		// away and back still says which line moved.
		icon = styleAccent.Bold(true).Render("✓")
	case e.done:
		icon = styleOK.Render("✓")
	case e.running && o.spinner != "":
		icon = styleAccent.Render(o.spinner)
	case e.running:
		icon = styleAccent.Render("▶")
	}
	name := styleTool.Bold(true).Render(e.call.Name)
	summary := summarizeInput(e.call.Name, e.inputJSON(), width-len(e.call.Name)-14, o.cwd)
	if o.renderer != nil {
		if r, ok := o.renderer(e.call.Name); ok {
			if lines := r.RenderCall(json.RawMessage(e.inputJSON()), tool.RenderContext{Expanded: o.expand(e)}); len(lines) > 0 {
				summary = truncateLine(strings.Join(lines, " · "), width-len(e.call.Name)-14)
			}
		}
	}
	pendingHere := o.pending != nil && o.pending.call.ID == e.call.ID
	if !e.done && !e.running && !pendingHere && e.input.Len() > 0 {
		// The model is still streaming the call's input. For a file write
		// that can be a minute of silence otherwise; say what is being
		// written and how much has arrived.
		if s := streamingSummary(e.call.Name, e.input.String(), o.cwd); s != "" {
			summary = s
		}
	}
	sb.WriteString(icon + " " + name + " " + styleDim.Render(summary))
	switch {
	case e.dur != "":
		sb.WriteString(styleFaint.Render(" " + e.dur))
	case e.running && !e.started.IsZero():
		if el := time.Since(e.started); el > time.Second {
			sb.WriteString(styleFaint.Render(" " + shortDuration(el)))
		}
	}
	if !e.done && !e.running && !pendingHere && o.preview != nil && (e.call.Name == "edit_file" || e.call.Name == "write_file") {
		// The arguments are complete but the call has not run: show what
		// it would do now, while the model is still writing the rest of
		// the turn, rather than after the fact.
		if raw := e.inputJSON(); json.Valid([]byte(raw)) {
			if e.livePreviewFor != raw {
				e.livePreview, e.livePreviewFor = o.preview(e.call.Name, []byte(raw)), raw
			}
			if e.livePreview != "" {
				sb.WriteString("\n" + renderPreview(e.livePreview, width, o.lines(12), !o.expand(e)))
			}
		}
	}
	if o.pending != nil && o.pending.call.ID == e.call.ID {
		previewMax, inputMax := pendingPreviewLines, pendingInputLines
		if o.expand(e) {
			previewMax, inputMax = 400, 400
		}
		if o.pending.call.Preview != "" {
			sb.WriteString("\n" + renderPreview(o.pending.call.Preview, width, previewMax, !o.expand(e)))
		} else if full := prettyInput(e.inputJSON()); full != "" && e.call.Name != "bash" {
			sb.WriteString("\n" + styleDim.Render(indent(clipTool(full, inputMax, !o.expand(e)), "  ")))
		}
		return sb.String()
	}
	if e.done && !e.isError && e.call.Preview != "" && !e.denied {
		max := o.lines(12)
		if o.expand(e) {
			max = 200
		}
		sb.WriteString("\n" + renderPreview(e.call.Preview, width, max, !o.expand(e)))
	}
	if e.done && e.output != "" {
		max := o.lines(8)
		if o.expand(e) {
			max = expandedLines
		}
		body, diag := splitDiagnostics(strings.TrimRight(e.output, "\n"))
		out := ""
		switch lines := t.toolLines(e, body, o, max); {
		case e.isError:
			// A failed build says what went wrong at the end. Keeping the
			// head kept the compiler's preamble and dropped the error.
			out = tailLines(body, max)
		case lines != nil:
			// The tool said how its result reads (see tool.Renderer).
			out = clipTool(strings.Join(lines, "\n"), max, !o.expand(e))
		default:
			out = clipTool(body, max, !o.expand(e))
		}
		style := styleFaint
		if e.isError {
			style = styleErr
		}
		if out != "" {
			sb.WriteString("\n" + style.Render(indent(lipgloss.NewStyle().Width(width-4).Render(out), "  ")))
		}
		if diag != "" {
			// Language-server problems after an edit are never clipped
			// and never faint: they are the thing to read next.
			sb.WriteString("\n" + styleErr.Render(indent(lipgloss.NewStyle().Width(width-4).Render(clipNote(diag, 12, false)), "  ")))
		}
	}
	return sb.String()
}

// streamingSummary describes a tool call whose input is still arriving:
// the path, once the model has written it, and how much content so far.
func streamingSummary(name, partial, cwd string) string {
	path := ""
	if m := partialPathRe.FindStringSubmatch(partial); m != nil {
		path = relTo(cwd, m[1])
	}
	switch name {
	case "write_file", "edit_file":
		if path == "" {
			return "…"
		}
		body := partial
		if i := strings.Index(partial, `"content"`); i >= 0 {
			body = partial[i:]
		} else if i := strings.Index(partial, `"new_string"`); i >= 0 {
			body = partial[i:]
		} else if i := strings.Index(partial, `"edits"`); i >= 0 {
			body = partial[i:]
		} else {
			return path
		}
		lines := strings.Count(body, `\n`) + 1
		verb := "writing"
		if name == "edit_file" {
			verb = "editing"
		}
		return fmt.Sprintf("%s · %s %d lines (%s) so far", path, verb, lines, shortBytes(int64(len(body))))
	case "bash":
		if m := partialCommandRe.FindStringSubmatch(partial); m != nil {
			return truncateLine(strings.ReplaceAll(m[1], `\n`, " ⏎ "), 80) + "…"
		}
	}
	return ""
}

var (
	partialPathRe    = regexp.MustCompile(`"path"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	partialCommandRe = regexp.MustCompile(`"command"\s*:\s*"((?:[^"\\]|\\.)*)`)
)

// errorBlock renders an error as "✗ first line" with every following line
// indented two columns under it, whether the break came from the message
// (friendlyError adds a second line saying what to do) or from wrapping.
// The whole block is one colour: an error is one thing to read.
func errorBlock(text string, width int) string {
	var src []string
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		src = append(src, strings.TrimLeft(l, " \t"))
	}
	body := unpad(lipgloss.NewStyle().Width(width - 4).Render(strings.Join(src, "\n")))
	lines := strings.Split(body, "\n")
	for i := range lines {
		if i == 0 {
			lines[i] = "✗ " + lines[i]
			continue
		}
		lines[i] = "  " + strings.TrimLeft(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}

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

// unpad strips the trailing spaces lipgloss pads every wrapped line with.
func unpad(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// isAttachNote reports a notice about what the harness attached to the
// request: skills, recalled memory, the repository map.
func isAttachNote(text string) bool {
	for _, p := range []string{"skills attached", "memory recalled", "repository map attached"} {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// splitDiagnostics separates a tool result from the language-server
// diagnostics appended to it, so problems can be shown on their own.
func splitDiagnostics(out string) (body, diag string) {
	i := strings.Index(out, "[diagnostics]\n")
	if i < 0 {
		return out, ""
	}
	return strings.TrimRight(out[:i], "\n"), "✗ " + strings.TrimSpace(strings.TrimPrefix(out[i:], "[diagnostics]\n"))
}

// thinkingView shows the tail of the reasoning while it streams and folds
// it to a single line once the model moves on, so it never swamps the
// transcript. The streaming view is capped at three lines so the fold
// costs the transcript two lines rather than seven.
func thinkingView(text string, done bool, took time.Duration) string {
	txt := strings.TrimSpace(text)
	if txt == "" {
		return "· thinking"
	}
	lines := strings.Split(txt, "\n")
	if done {
		first := cutWords(strings.TrimSpace(lines[0]), 100)
		head := "· thought"
		// Only when it was long enough to have been waited for: "thought
		// for 40ms" is noise on every line.
		if took >= time.Second {
			head = "· thought for " + shortDuration(took)
		}
		if len(lines) > 1 {
			return fmt.Sprintf("%s: %s (%d lines)", head, first, len(lines))
		}
		return head + ": " + first
	}
	// At most three lines while streaming. Eight was enough to make the
	// transcript jump by seven lines the moment the model moved on and the
	// thought folded to one.
	const shown = 3
	if len(lines) > shown {
		lines = append([]string{fmt.Sprintf("· thinking… (%d earlier lines)", len(lines)-shown+1)}, lines[len(lines)-shown+1:]...)
		return strings.Join(lines, "\n")
	}
	return "· " + strings.Join(lines, "\n")
}

// flashFor is how long a finished tool line stays bright.
const flashFor = 400 * time.Millisecond

// How much of a call awaiting approval is shown before ctrl+o expands it.
const (
	pendingPreviewLines = 60
	pendingInputLines   = 30
)

// pendingExpandable reports whether ctrl+o would actually show more of a
// call awaiting approval, so the key line can offer it only when it would
// do something.
func pendingExpandable(c agent.ToolCall) bool {
	if c.Preview != "" {
		return previewLines(c.Preview) >= pendingPreviewLines+clipSlack
	}
	if c.Name == "bash" {
		return false
	}
	if full := prettyInput(string(c.Input)); full != "" {
		return len(strings.Split(full, "\n")) >= pendingInputLines+clipSlack
	}
	return false
}

// previewLines counts the lines renderPreview would draw for a preview.
func previewLines(preview string) int {
	n := 0
	for _, l := range strings.Split(strings.TrimRight(preview, "\n"), "\n") {
		if strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") {
			continue
		}
		n++
	}
	return n
}

// renderPreview draws a unified diff with line numbers, elided gaps and
// word-level highlighting (see diff.go), or shows plain preview text as
// it is when the preview is not a diff.
func renderPreview(preview string, width, maxLines int, expandable bool) string {
	if hunks, ok := parseUnifiedDiff(preview); ok {
		return renderDiff(hunks, width, maxLines, expandable)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(preview, "\n"), "\n") {
		if strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") {
			continue // the file name is already on the tool line
		}
		lines = append(lines, l)
	}
	hidden := 0
	if len(lines) >= maxLines+clipSlack {
		hidden, lines = len(lines)-maxLines, lines[:maxLines]
	}
	out := make([]string, 0, len(lines)+1)
	for _, l := range lines {
		// Column-aware: a byte slice here cut multi-byte runes in half and
		// printed the halves as replacement characters.
		l = fitLine(l, width-4)
		switch {
		case strings.HasPrefix(l, "+"):
			out = append(out, "  "+styleDiffAdd.Render(l))
		case strings.HasPrefix(l, "-"):
			out = append(out, "  "+styleDiffDel.Render(l))
		case strings.HasPrefix(l, "@@"):
			out = append(out, "  "+styleAccent.Render(l))
		default:
			out = append(out, "  "+styleDim.Render(l))
		}
	}
	if hidden > 0 {
		out = append(out, styleFaint.Render("  "+moreNote(hidden, expandable)))
	}
	return strings.Join(out, "\n")
}

func (e *entry) inputJSON() string {
	if e.input.Len() > 0 {
		return e.input.String()
	}
	return string(e.call.Input)
}

// summarizeInput shows the most useful argument on one line. Paths are
// shown relative to cwd: the model often passes them absolute, and an
// absolute path truncated to fit loses the one part that matters, the
// file name.
func summarizeInput(name, raw string, max int, cwd string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return truncateLine(raw, max)
	}
	for _, k := range []string{"command", "pattern", "path", "url", "description"} {
		if v, ok := m[k].(string); ok {
			extra := ""
			if k == "command" || k == "description" {
				// A command redirecting into the project carried the whole
				// absolute path, which is the same for every call and ate
				// the width the command itself needed: the prompt read
				// "allow bash seq 1 400 > /tmp/claude-1000/-home-driver-…"
				// and never reached the file name.
				v = shortenPaths(cwd, v)
			}
			if k == "pattern" {
				if p, ok := m["path"].(string); ok && p != "" {
					extra = " in " + relTo(cwd, p)
				}
			}
			if k == "path" {
				v = relTo(cwd, v)
				if off, ok := m["offset"].(float64); ok && off > 1 {
					extra = fmt.Sprintf(" from line %d", int(off))
				}
			}
			return truncateLine(v+extra, max)
		}
	}
	return truncateLine(raw, max)
}

// shortenPaths rewrites absolute paths inside a sentence the way tool
// lines write them. Notices and verdicts are composed by the agent,
// which only ever knows absolute paths, so a single file name could
// wrap the line twice and bury the sentence it belonged to.
func shortenPaths(cwd, s string) string {
	if cwd != "" {
		s = strings.ReplaceAll(s, cwd+string(filepath.Separator), "")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home+string(filepath.Separator), "~"+string(filepath.Separator))
	}
	return s
}

// relTo shows p relative to cwd when it lies under it, with the home
// directory as ~ otherwise. A relative path is returned as it is.
func relTo(cwd, p string) string {
	if cwd == "" || !filepath.IsAbs(p) {
		return p
	}
	if r, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return shortPath(p)
}

func prettyInput(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return raw
	}
	return string(b)
}

// cutWords shortens s to at most max characters at a word boundary, so a
// folded thought never ends mid-word.
func cutWords(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := max - 1
	if i := strings.LastIndex(string(r[:cut]), " "); i > max/2 {
		cut = i
	}
	return strings.TrimRight(string(r[:cut]), " ,;:") + "…"
}

// fitLine cuts a rendered line to max columns, counting columns rather
// than bytes so colour codes and wide runes are handled.
func fitLine(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= max {
		return s
	}
	return ansi.Truncate(s, max, "…")
}

func truncateLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", "⏎ ")
	if max < 10 {
		max = 10
	}
	// Width-aware: colour codes take no columns and a rune is never split.
	return ansi.Truncate(s, max, "…")
}

// clipSlack is how many lines a collapse must save to be worth it: the
// "… (N more lines, ctrl+o to expand)" line costs one line and a decision,
// so hiding two or three is a worse deal than showing them.
const clipSlack = 4

// expandedLines is how much of a tool result ctrl+o shows. It was sixty,
// which left four hundred and forty lines of a five-hundred line command
// behind a key that, pressed again, folds the entry instead.
const expandedLines = 400

// clipTool clips a tool result to max lines, but only when that hides at
// least clipSlack of them.
func clipTool(s string, max int, expandable bool) string {
	if len(strings.Split(s, "\n")) < max+clipSlack {
		return s
	}
	return clipNote(s, max, expandable)
}

// tailLines keeps the last max lines of s: what an error ends with is
// what says why it failed.
func tailLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	if len(lines) < max+clipSlack {
		return s
	}
	return fmt.Sprintf("… +%d lines\n", len(lines)-max) + strings.Join(lines[len(lines)-max:], "\n")
}

// moreNote stands for the lines a block does not show. ctrl+o folds an
// entry that is already expanded, so offering it there named a key that
// does the opposite of what the line promised.
func moreNote(n int, expandable bool) string {
	if expandable {
		return fmt.Sprintf("… (%d more lines, ctrl+o to expand)", n)
	}
	return fmt.Sprintf("… (%d more lines; the model received all of it)", n)
}

func clip(s string, lines int) string { return clipNote(s, lines, true) }

func clipNote(s string, lines int, expandable bool) string {
	parts := strings.Split(s, "\n")
	if len(parts) <= lines {
		return s
	}
	return strings.Join(parts[:lines], "\n") + "\n" + moreNote(len(parts)-lines, expandable)
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func shortDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

func shortTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// renderCode shows lines verbatim with diff colouring by prefix and no
// wrapping; long lines are cut at the width.
func renderCode(text string, width int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if width > 6 && lipgloss.Width(l) > width-2 {
			// truncateLine already marks the cut; adding another ellipsis
			// here ended every long line in "……".
			l = truncateLine(l, width-2)
		}
		switch {
		case strings.HasPrefix(l, "+++ ") || strings.HasPrefix(l, "--- "):
			out = append(out, styleDim.Render(l))
		case strings.HasPrefix(l, "+"):
			out = append(out, styleDiffAdd.Render(l))
		case strings.HasPrefix(l, "-"):
			out = append(out, styleDiffDel.Render(l))
		case strings.HasPrefix(l, "@@"):
			out = append(out, styleAccent.Render(l))
		case strings.HasPrefix(l, "▶"):
			out = append(out, styleAccent.Render(l))
		case strings.HasPrefix(l, "#"):
			out = append(out, styleKey.Bold(true).Render(l))
		default:
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// expandable reports whether showing this entry in full would say more
// than the folded form does.
func (e *entry) expandable() bool {
	switch e.kind {
	case entryTool:
		return e.output != "" || e.call.Preview != "" || e.input.Len() > 0
	case entryThinking:
		return e.done && strings.TrimSpace(e.text) != ""
	}
	return false
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

// toolLines asks the tool how its result should read. A tool that says
// nothing — every MCP tool, and every tool that has nothing special to
// say — falls back to the generic fold.
func (t *transcript) toolLines(e *entry, body string, o renderOpts, max int) []string {
	if o.renderer == nil || e.isError {
		return nil
	}
	r, ok := o.renderer(e.call.Name)
	if !ok {
		return nil
	}
	return r.RenderResult(json.RawMessage(e.inputJSON()), body, tool.RenderContext{
		Expanded: o.expand(e),
		Max:      max,
		Prev:     t.previousResult(e),
		Cwd:      o.cwd,
	})
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

// thoughtFor is how long the model spent on a finished thought, or zero
// when the entry did not record it (a resumed conversation).
func (e *entry) thoughtFor() time.Duration {
	if e.started.IsZero() || e.ended.IsZero() || !e.ended.After(e.started) {
		return 0
	}
	return e.ended.Sub(e.started)
}

// sanitize strips the control characters from text that came out of the
// repository — a tool's argument, a server's name, a skill's description
// — before it is shown in a prompt. A C0 or C1 sequence in a file the
// model was asked to look at could otherwise move the cursor, repaint the
// prompt, or hide what is actually being approved.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return -1
		}
		return r
	}, s)
}
