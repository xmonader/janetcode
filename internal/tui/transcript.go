package tui

import (
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
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
	width   int
	resized bool
	pending *permissionMsg
	spinner string // current spinner frame for running tools
	verbose bool   // expand tool output
	cwd     string // paths in tool lines are shown relative to it
}

// transcript is the ordered list of things shown in the viewport.
type transcript struct {
	entries   []*entry
	renderer  *glamour.TermRenderer
	rWidth    int
	styleName string // glamour standard style: "dark" or "light"
}

// liveRenderInterval throttles markdown rendering of streaming text.
const liveRenderInterval = 80 * time.Millisecond

func newTranscript() *transcript { return newTranscriptStyled("dark") }

func newTranscriptStyled(style string) *transcript { return &transcript{styleName: style} }

func (t *transcript) add(e *entry) *entry {
	t.entries = append(t.entries, e)
	return e
}

func (t *transcript) info(s string)   { t.add(&entry{kind: entryInfo, text: s}) }
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
			e = t.add(&entry{kind: entryThinking})
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
		e := t.findTool(ev.Call.ID)
		if e == nil {
			e = t.add(&entry{kind: entryTool, call: *ev.Call})
		}
		e.call = *ev.Call
		e.running = false
		e.done = true
		e.output = ev.Output
		e.isError = ev.IsError
		if ev.Duration > 0 {
			e.dur = shortDuration(ev.Duration)
		}
	case agent.EvTextBlockDone:
		t.closeOpen()
	case agent.EvTurnDone:
		t.closeOpen()
	case agent.EvCompacted:
		t.closeOpen()
		t.info("⇣ " + ev.Text + " · expand_output brings archived output back; /context shows what remains")
	case agent.EvVerdict:
		// The one line that says whether the turn showed its work. It was
		// dropped here for a long time while headless mode printed it.
		t.closeOpen()
		t.add(&entry{kind: entryVerdict, text: ev.Text, isError: ev.IsError})
	case agent.EvNotice:
		t.closeOpen()
		if isAttachNote(ev.Text) {
			t.info("📎 " + ev.Text)
		} else {
			t.info("⚑ " + ev.Text)
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

// closeOpen marks streaming text/thinking entries finished so the next delta
// starts a new entry and the finished one gets rendered as final markdown.
func (t *transcript) closeOpen() {
	for i := len(t.entries) - 1; i >= 0; i-- {
		e := t.entries[i]
		if e.kind == entryAssistant || e.kind == entryThinking {
			if !e.done {
				e.done = true
				e.final = false
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
				if r, ok := results[b.ID]; ok {
					e.output, e.isError = r.Text, r.IsError
				}
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
	if t.renderer == nil || t.rWidth != width {
		r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(t.styleName), glamour.WithWordWrap(width-2))
		if err == nil {
			t.renderer = r
			t.rWidth = width
		}
	}
	now := time.Now()
	var sb strings.Builder
	for i, e := range t.entries {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if t.needsRender(e, o, now) {
			start := time.Now()
			e.rendered = t.renderEntry(e, width, o)
			e.renderCost = time.Since(start)
			e.width = width
			e.final = e.done
			e.renderedAt = now
			e.renderedN = len(e.text)
			e.verbose = o.verbose
		}
		sb.WriteString(e.rendered)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (t *transcript) needsRender(e *entry, o renderOpts, now time.Time) bool {
	if e.rendered == "" || e.width != o.width || e.verbose != o.verbose {
		return true
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
	wrap := lipgloss.NewStyle().Width(width - 2)
	switch e.kind {
	case entryBanner:
		return styleDim.Render(wrap.Render(e.text))
	case entryInfo:
		return styleDim.Render(wrap.Render(e.text))
	case entryCode:
		return renderCode(e.text, width)
	case entryError:
		return styleErr.Render(wrap.Render("✗ " + e.text))
	case entryVerdict:
		style := styleWarn
		switch {
		case strings.HasPrefix(e.text, "✓"):
			style = styleOK
		case strings.HasPrefix(e.text, "✗"):
			style = styleErr
		}
		return style.Render(wrap.Render(e.text))
	case entryUser:
		body := lipgloss.NewStyle().Width(width - 4).Render(e.text)
		return styleUser.Render("❯ ") + styleUserBody.Render(strings.ReplaceAll(body, "\n", "\n  "))
	case entryThinking:
		return styleFaint.Italic(true).Render(wrap.Render(thinkingView(e.text, e.done)))
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
	case e.done:
		icon = styleOK.Render("✓")
	case e.running && o.spinner != "":
		icon = styleAccent.Render(o.spinner)
	case e.running:
		icon = styleAccent.Render("▶")
	}
	name := styleTool.Bold(true).Render(e.call.Name)
	summary := summarizeInput(e.call.Name, e.inputJSON(), width-len(e.call.Name)-14, o.cwd)
	sb.WriteString(icon + " " + name + " " + styleDim.Render(summary))
	switch {
	case e.dur != "":
		sb.WriteString(styleFaint.Render(" " + e.dur))
	case e.running && !e.started.IsZero():
		if el := time.Since(e.started); el > time.Second {
			sb.WriteString(styleFaint.Render(" " + shortDuration(el)))
		}
	}
	if o.pending != nil && o.pending.call.ID == e.call.ID {
		if o.pending.call.Preview != "" {
			sb.WriteString("\n" + renderPreview(o.pending.call.Preview, width, 60))
		} else if full := prettyInput(e.inputJSON()); full != "" && e.call.Name != "bash" {
			sb.WriteString("\n" + styleDim.Render(indent(clip(full, 30), "  ")))
		}
		return sb.String()
	}
	if e.done && !e.isError && e.call.Preview != "" && !e.denied {
		max := 12
		if o.verbose {
			max = 200
		}
		sb.WriteString("\n" + renderPreview(e.call.Preview, width, max))
	}
	if e.done && e.output != "" {
		max := 4
		if o.verbose {
			max = 60
		}
		if e.isError {
			max = 8
		}
		if e.call.Name == "todo" {
			max = 20 // the plan is the point; show it whole
		}
		body, diag := splitDiagnostics(strings.TrimRight(e.output, "\n"))
		out := clip(body, max)
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
			sb.WriteString("\n" + styleErr.Render(indent(lipgloss.NewStyle().Width(width-4).Render(clip(diag, 12)), "  ")))
		}
	}
	return sb.String()
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
// transcript.
func thinkingView(text string, done bool) string {
	txt := strings.TrimSpace(text)
	if txt == "" {
		return "· thinking"
	}
	lines := strings.Split(txt, "\n")
	if done {
		first := cutWords(strings.TrimSpace(lines[0]), 100)
		if len(lines) > 1 {
			return fmt.Sprintf("· thought: %s (%d lines)", first, len(lines))
		}
		return "· thought: " + first
	}
	const tail = 8
	if len(lines) > tail {
		lines = append([]string{fmt.Sprintf("· thinking… (%d earlier lines)", len(lines)-tail)}, lines[len(lines)-tail:]...)
		return strings.Join(lines, "\n")
	}
	return "· " + strings.Join(lines, "\n")
}

// renderPreview colours a unified diff (or shows plain preview text).
func renderPreview(preview string, width, maxLines int) string {
	lines := strings.Split(strings.TrimRight(preview, "\n"), "\n")
	var out []string
	shown := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") {
			continue
		}
		if shown >= maxLines {
			out = append(out, styleFaint.Render(fmt.Sprintf("  … (%d more lines, ctrl+o to expand)", len(lines)-shown-2)))
			break
		}
		shown++
		if len(l) > width-4 {
			l = l[:width-5] + "…"
		}
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

func truncateLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", "⏎ ")
	if max < 10 {
		max = 10
	}
	// Width-aware: colour codes take no columns and a rune is never split.
	return ansi.Truncate(s, max, "…")
}

func clip(s string, lines int) string {
	parts := strings.Split(s, "\n")
	if len(parts) <= lines {
		return s
	}
	return strings.Join(parts[:lines], "\n") + fmt.Sprintf("\n… (%d more lines, ctrl+o to expand)", len(parts)-lines)
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
			l = truncateLine(l, width-3) + "…"
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
