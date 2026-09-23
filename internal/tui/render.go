package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/flavor"
	"github.com/xmonader/janetcode/internal/mermaid"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// The transcript's rendering: the pass over the entries that produces
// the text the viewport shows, what one entry looks like in each of its
// kinds, and the markdown, code and line-fitting the text goes through
// on the way. Nothing here changes the conversation; it only draws it.

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
	// noMermaid keeps ```mermaid blocks as source instead of drawing
	// them: Off["mermaid"] / JANETCODE_OFF=mermaid, the pre-feature
	// behaviour.
	noMermaid bool
	// renderer finds a tool that knows how to present itself.
	renderer func(name string) (tool.Renderer, bool)
	// now is the frame's clock, so every entry in one frame agrees about
	// which finished calls are still flashing. Zero means "ask the clock",
	// for the callers that render one entry outside a frame.
	now time.Time
}

// at is the frame's clock.
func (o renderOpts) at() time.Time {
	if o.now.IsZero() {
		return time.Now()
	}
	return o.now
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
		r, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle(t.styles.col, t.styleName == "dark")), glamour.WithWordWrap(width-2))
		if err == nil {
			t.renderer = r
			t.rWidth = width
		}
	}
	now := time.Now()
	o.now = now
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
			if e.kind == entryTool {
				e.toolStamp = toolStamp(e, o)
			}
		} else if e.kind == entryTool && e.toolBody != "" && (e.running || e.flashed || flashing(t.styles, e, now)) {
			// Spinner, elapsed and the finished-call flash are chrome:
			// splice them onto the cached body rather than rebuilding the
			// result. e.flashed carries the one frame after the flash has
			// faded, where the cached render still holds the bright mark
			// and nothing else would ask for it to be redrawn.
			e.rendered = applyToolChrome(e.toolBody, toolIcon(t.styles, e, o), toolElapsed(t.styles, e, o))
		}
		if e.kind == entryTool {
			e.flashed = flashing(t.styles, e, now)
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
		// Content (input, output, pending preview), not spinner/elapsed.
		return e.toolStamp != toolStamp(e, o)
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
		return t.styles.dim.Render(wrap(e.text))
	case entryInfo:
		return t.styles.dim.Render(wrap(e.text))
	case entryTip:
		return t.styles.faint.Render(wrap("tip · " + e.text))
	case entryCode:
		return renderCode(t.styles, e.text, width)
	case entryError:
		return t.styles.err.Render(errorBlock(e.text, width))
	case entryVerdict:
		style := t.styles.warn
		switch present.VerdictTone(e.text) {
		case present.ToneOK:
			style = t.styles.ok
		case present.ToneBad:
			style = t.styles.err
		}
		return style.Render(wrap(e.text))
	case entryUser:
		body := unpad(lipgloss.NewStyle().Width(width - 4).Render(e.text))
		return t.styles.user.Render("❯ ") + t.styles.userBody.Render(strings.ReplaceAll(body, "\n", "\n  "))
	case entryThinking:
		// Faint, never italic: many terminals have no italic face and
		// draw SGR italic as reverse video.
		if o.expand(e) && e.done {
			return t.styles.faint.Render(wrap("· thought: " + strings.TrimSpace(e.text)))
		}
		return t.styles.faint.Render(wrap(thinkingView(e.text, e.done, e.thoughtFor(), e.started)))
	case entryAssistant:
		return t.renderMarkdown(e, width, o)
	case entryTool:
		return t.renderTool(e, width, o)
	}
	return e.text
}

// renderMarkdown renders through glamour block by block, caching every
// block whose source has not changed. While streaming only the last block
// is re-rendered, which keeps the cost proportional to the tail rather than
// the whole message.
func (t *transcript) renderMarkdown(e *entry, width int, o renderOpts) string {
	text := e.text
	if strings.TrimSpace(text) == "" {
		if e.done {
			return ""
		}
		return t.styles.faint.Render("▍")
	}
	if t.renderer == nil {
		return lipgloss.NewStyle().Width(width - 2).Render(text)
	}
	blocks := splitMarkdownBlocks(text)
	if len(e.chunks) > len(blocks) {
		e.chunks = e.chunks[:len(blocks)]
	}
	expanded := o.expand(e)
	var sb strings.Builder
	for i, src := range blocks {
		last := i == len(blocks)-1
		partial := last && !e.done
		if partial && strings.Count(src, "```")%2 == 1 {
			// Close an unterminated fence so the partial code block still
			// gets highlighted instead of swallowing the rest.
			src += "\n```"
		}
		// The cache is consulted before anything is drawn: a diagram laid
		// out up front was thrown away on every hit, so a message holding
		// a mermaid block re-parsed and re-laid it out on every frame of
		// the stream.
		// Blocks were separated by a blank line in the source and are on
		// screen too. The renderer's own margins were uneven (a code block
		// padded above, nothing below), so a paragraph after a code block
		// sat right against it; each block is trimmed and the gap is ours.
		if i > 0 {
			sb.WriteString("\n")
		}
		if i < len(e.chunks) && e.chunks[i].src == src && e.chunks[i].width == width &&
			e.chunks[i].expanded == expanded && e.chunks[i].partial == partial {
			sb.WriteString(e.chunks[i].out)
			continue
		}
		out := ""
		if !partial {
			out = t.renderDiagram(src, width, expanded, o.noMermaid)
		}
		if out == "" {
			var err error
			out, err = t.renderer.Render(joinListSoftBreaks(src))
			if err != nil {
				out = lipgloss.NewStyle().Width(width - 2).Render(src)
			}
		}
		out = trimBlankLines(out) + "\n"
		if i < len(e.chunks) {
			e.chunks[i] = mdChunk{src, out, width, expanded, partial}
		} else {
			e.chunks = append(e.chunks, mdChunk{src, out, width, expanded, partial})
		}
		sb.WriteString(out)
	}
	out := strings.TrimRight(sb.String(), "\n")
	if !e.done {
		out += t.styles.faint.Render(" ▍")
	}
	return out
}

// renderDiagram draws a ```mermaid block as a picture, or returns ""
// for every block and every condition that has to keep the source: a
// block that is not mermaid, a diagram kind the parser does not fully
// support, a terminal too narrow or with only sixteen colours, and an
// entry the reader has expanded — ctrl+o (and /verbose) swap the picture
// for the source it was drawn from, the same way they open a folded tool
// result, so the source is never lost.
func (t *transcript) renderDiagram(block string, width int, expanded, off bool) string {
	body, ok := mermaidFence(block)
	if !ok || expanded || off || t.styles.ansiOnly {
		return ""
	}
	// The two columns glamour indents a document by, on both sides.
	lines, ok := mermaid.Render(body, width-4)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString("  " + diagramLine(t.styles, l) + "\n")
	}
	sb.WriteString("  " + t.styles.faint.Render("diagram · ctrl+o shows the mermaid source"))
	return sb.String()
}

// mermaidFence returns the body of a fenced mermaid block, and false for
// anything else.
func mermaidFence(block string) (string, bool) {
	lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
	if len(lines) < 3 {
		return "", false
	}
	head := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(head, "```") || strings.TrimSpace(strings.TrimPrefix(head, "```")) != "mermaid" {
		return "", false
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return "", false
	}
	return strings.Join(lines[1:len(lines)-1], "\n"), true
}

// diagramLine colours one line of a diagram: the frame in dim, the
// labels in the body colour, so the boxes recede and the words read.
func diagramLine(sty *styles, line string) string {
	var sb strings.Builder
	var run []rune
	frame := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		if frame {
			sb.WriteString(sty.dim.Render(string(run)))
		} else {
			sb.WriteString(string(run))
		}
		run = run[:0]
	}
	for _, r := range line {
		if f := isFrame(r); f != frame {
			flush()
			frame = f
		}
		run = append(run, r)
	}
	flush()
	return sb.String()
}

// isFrame reports whether a rune is part of a diagram's frame rather
// than of a label. A space belongs to whichever run it is in, so a gap
// inside a box does not split the label into two styled pieces.
func isFrame(r rune) bool {
	switch r {
	case '─', '│', '┌', '┐', '└', '┘', '├', '┤', '┬', '┴', '┼', '╌', '▶', '◀', '▲', '▼':
		return true
	}
	return false
}

// listMarker matches the start of a list item: a bullet or a number.
var listMarker = regexp.MustCompile(`^\s*([-*+]|\d{1,9}[.)])\s`)

// joinListSoftBreaks joins the soft line breaks inside the list items of
// one markdown block. glamour reflows a paragraph's soft breaks but keeps
// a list item's as hard ones, so a line break in the model's source landed
// mid-sentence in the transcript — "…multi-byte characters" on one line,
// "when truncating…" on the next — however wide the window. A line that
// starts another item, a heading, a quote, a table row or a fence is not
// a continuation, and a hard break (two trailing spaces or a backslash)
// is kept. Blocks that are not lists, or that hold a fence, are returned
// as they are.
func joinListSoftBreaks(block string) string {
	lines := strings.Split(block, "\n")
	if !listMarker.MatchString(lines[0]) || strings.Contains(block, "```") || strings.Contains(block, "~~~") {
		return block
	}
	out := []string{lines[0]}
	for _, l := range lines[1:] {
		prev := out[len(out)-1]
		trimmed := strings.TrimSpace(l)
		hard := strings.HasSuffix(prev, "  ") || strings.HasSuffix(prev, "\\")
		starts := trimmed == "" || listMarker.MatchString(l) || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "|") || strings.HasPrefix(strings.TrimSpace(prev), "|")
		if hard || starts {
			out = append(out, l)
			continue
		}
		out[len(out)-1] = strings.TrimRight(prev, " ") + " " + trimmed
	}
	return strings.Join(out, "\n")
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

// thinkingView shows the tail of the reasoning while it streams and folds
// it to a single line once the model moves on, so it never swamps the
// transcript. The streaming view is capped at three lines so the fold
// costs the transcript two lines rather than seven.
func thinkingView(text string, done bool, took time.Duration, started time.Time) string {
	txt := strings.TrimSpace(text)
	if txt == "" {
		if done {
			return "· thinking"
		}
		return "· " + flavor.ForPhase("thinking", time.Now(), started)
	}
	lines := strings.Split(txt, "\n")
	if done {
		first := cutWords(strings.TrimSpace(lines[0]), 100)
		head := "· thought"
		// Only when it was long enough to have been waited for: "thought
		// for 40ms" is noise on every line.
		if took >= time.Second {
			head = "· thought for " + textfmt.Duration(took)
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

// renderCode shows lines verbatim with diff colouring by prefix and no
// wrapping; long lines are cut at the width.
func renderCode(sty *styles, text string, width int) string {
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
			out = append(out, sty.dim.Render(l))
		case strings.HasPrefix(l, "+"):
			out = append(out, sty.diffAdd.Render(l))
		case strings.HasPrefix(l, "-"):
			out = append(out, sty.diffDel.Render(l))
		case strings.HasPrefix(l, "@@"):
			out = append(out, sty.accent.Render(l))
		case strings.HasPrefix(l, "▶"):
			out = append(out, sty.accent.Render(l))
		case strings.HasPrefix(l, "#"):
			out = append(out, sty.key.Bold(true).Render(l))
		default:
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// unpad strips the trailing spaces lipgloss pads every wrapped line with.
func unpad(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
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
	if r := textfmt.RelTo(cwd, p); r != p {
		return r
	}
	return shortPath(p)
}

// cutWords shortens s to at most max characters at a word boundary, so a
// folded thought never ends mid-word. The search for that boundary counts
// runes, not bytes: strings.LastIndex answers in bytes, and using its
// answer as a rune index cut a thought in a non-Latin script short by
// two thirds — or, once the byte index passed the rune count, panicked
// with a slice out of range on a model's own reasoning.
func cutWords(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := max - 1
	for i := cut - 1; i > max/2; i-- {
		if r[i] == ' ' {
			cut = i
			break
		}
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

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// trimBlankLines drops the lines at either end of a rendered block that
// show nothing: the renderer pads its margins with styled spaces, which a
// plain Trim of newlines leaves in place.
func trimBlankLines(out string) string {
	lines := strings.Split(out, "\n")
	blank := func(l string) bool { return strings.TrimSpace(ansi.Strip(l)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
