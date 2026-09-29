package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// How one tool call is drawn: the chrome around it (the state glyph, the
// elapsed time, the fold marks), the body — arguments while they stream,
// a preview while it waits for approval, output once it has run — and the
// clipping that keeps a thousand-line result to a few lines until ctrl+o
// asks for the rest.

// toolStamp fingerprints a tool entry's content. Spinner and live elapsed
// are chrome and are not in it, so a tick does not rebuild the body.
func toolStamp(e *entry, o renderOpts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %d %t %t %t %t %q %s",
		e.input.Len(), len(e.output), e.running, e.done, e.denied, e.isError, e.dur, e.call.Name)
	if o.pending != nil && o.pending.call.ID == e.call.ID {
		fmt.Fprintf(&b, " p%d", len(o.pending.call.Preview))
		if o.expand(e) {
			b.WriteByte('x')
		}
	}
	if e.livePreviewFor != "" {
		b.WriteByte(' ')
		b.WriteString(e.livePreviewFor)
	}
	return b.String()
}

func toolIcon(sty *styles, e *entry, o renderOpts) string {
	icon := sty.tool.Render("⚙")
	switch {
	case e.denied:
		icon = sty.err.Render("⊘")
	case e.done && e.isError:
		icon = sty.err.Render("✗")
	case flashing(sty, e, o.at()):
		// A call that has just finished flashes for a moment, so a glance
		// away and back still says which line moved.
		icon = sty.accent.Bold(true).Render("✓")
	case e.done:
		icon = sty.ok.Render("✓")
	case e.running && o.spinner != "":
		icon = sty.accent.Render(o.spinner)
	case e.running:
		icon = sty.accent.Render("▶")
	}
	return icon
}

func toolElapsed(sty *styles, e *entry, o renderOpts) string {
	switch {
	case e.dur != "" && flashing(sty, e, o.at()):
		// Bright for the same moment the mark is: the two are one flash,
		// and the text is the same text, so nothing moves.
		return sty.accent.Render(" " + e.dur)
	case e.dur != "":
		return sty.faint.Render(" " + e.dur)
	case e.running && !e.started.IsZero():
		if el := time.Since(e.started); el > time.Second {
			return sty.faint.Render(" " + textfmt.Duration(el))
		}
	}
	return ""
}

// applyToolChrome puts the icon and elapsed onto a cached tool body.
func applyToolChrome(body, icon, elapsed string) string {
	first, rest, found := strings.Cut(body, "\n")
	line := icon + " " + first + elapsed
	if found {
		return line + "\n" + rest
	}
	return line
}

func (t *transcript) renderTool(e *entry, width int, o renderOpts) string {
	var sb strings.Builder
	name := t.styles.tool.Bold(true).Render(e.call.Name)
	summary := present.ToolSummary(e.call.Name, e.inputJSON(), width-len(e.call.Name)-14, o.cwd)
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
	sb.WriteString(name + " " + t.styles.dim.Render(summary))
	if !e.done && !e.running && !pendingHere && o.preview != nil && (e.call.Name == "edit_file" || e.call.Name == "write_file" || e.call.Name == "notebook_edit") {
		// The arguments are complete but the call has not run: show what
		// it would do now, while the model is still writing the rest of
		// the turn, rather than after the fact.
		if raw := e.inputJSON(); json.Valid([]byte(raw)) {
			if e.livePreviewFor != raw {
				e.livePreview, e.livePreviewFor = o.preview(e.call.Name, []byte(raw)), raw
			}
			if e.livePreview != "" {
				sb.WriteString("\n" + renderPreview(t.styles, e.livePreview, width, o.lines(12), !o.expand(e)))
			}
		}
	}
	if o.pending != nil && o.pending.call.ID == e.call.ID {
		previewMax, inputMax := pendingPreviewLines, pendingInputLines
		if o.expand(e) {
			previewMax, inputMax = 400, 400
		}
		if o.pending.call.Preview != "" {
			sb.WriteString("\n" + renderPreview(t.styles, o.pending.call.Preview, width, previewMax, !o.expand(e)))
		} else if full := prettyInputFor(e.call.Name, e.inputJSON(), o.expand(e)); full != "" && e.call.Name != "bash" {
			sb.WriteString("\n" + t.styles.dim.Render(indent(clipTool(full, inputMax, !o.expand(e)), "  ")))
		}
		return t.finishTool(e, sb.String(), o)
	}
	if e.done && !e.isError && e.call.Preview != "" && !e.denied {
		max := o.lines(12)
		if o.expand(e) {
			max = 200
		}
		sb.WriteString("\n" + renderPreview(t.styles, e.call.Preview, width, max, !o.expand(e)))
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
		style := t.styles.faint
		if e.isError {
			style = t.styles.err
		}
		if out != "" {
			sb.WriteString("\n" + style.Render(indent(lipgloss.NewStyle().Width(width-4).Render(out), "  ")))
		}
		if diag != "" {
			// Language-server problems after an edit are never clipped
			// and never faint: they are the thing to read next.
			sb.WriteString("\n" + t.styles.err.Render(indent(lipgloss.NewStyle().Width(width-4).Render(clipNote(diag, 12, false)), "  ")))
		}
	}
	return t.finishTool(e, sb.String(), o)
}

func (t *transcript) finishTool(e *entry, body string, o renderOpts) string {
	e.toolBody = body
	return applyToolChrome(body, toolIcon(t.styles, e, o), toolElapsed(t.styles, e, o))
}

// streamingSummary is tool.StreamingSummary with the transcript's path
// shortening (relative under cwd, ~ for home).
func streamingSummary(name, partial, cwd string) string {
	return tool.StreamingSummary(name, partial, func(p string) string { return relTo(cwd, p) })
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

// flashFor is how long a finished tool line stays bright.
const flashFor = 400 * time.Millisecond

// flashing reports whether this call finished recently enough to be drawn
// brightly. It is a pure read of when the result arrived, so however many
// calls finish together they all flash from the same frame and fade in the
// same one — the model arms a single timer for the lot (see
// model.scheduleFlash).
//
// There is no flash at all on a terminal that has only the sixteen ANSI
// colours: the bright form would be the ordinary form, so it would say
// nothing, and there is nothing to schedule. A failed or denied call is
// not flashed either — its own mark already says what happened, and a
// celebration of a failure reads wrong.
func flashing(sty *styles, e *entry, now time.Time) bool {
	return !sty.ansiOnly && e.done && !e.isError && !e.denied &&
		!e.ended.IsZero() && now.Sub(e.ended) < flashFor
}

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
func renderPreview(sty *styles, preview string, width, maxLines int, expandable bool) string {
	if hunks, ok := parseUnifiedDiff(preview); ok {
		return renderDiff(sty, hunks, width, maxLines, expandable)
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
			out = append(out, "  "+sty.diffAdd.Render(l))
		case strings.HasPrefix(l, "-"):
			out = append(out, "  "+sty.diffDel.Render(l))
		case strings.HasPrefix(l, "@@"):
			out = append(out, "  "+sty.accent.Render(l))
		default:
			out = append(out, "  "+sty.dim.Render(l))
		}
	}
	if hidden > 0 {
		out = append(out, sty.faint.Render("  "+moreNote(hidden, expandable)))
	}
	return strings.Join(out, "\n")
}

// prettyInputFor is prettyInput with a third-party tool's argument values
// held back until the entry is expanded.
//
// summarizeInput already names the keys and not the values for an MCP
// tool, because those values often carry a credential the model copied
// out of context and the permission line lands in scrollback and in
// screenshots. The prompt body underneath it printed the whole JSON —
// so the thing the one-line summary was careful about was on screen two
// lines below it, and the comment saying the values were "one expand
// away" was describing something that was not true.
func prettyInputFor(name, raw string, expanded bool) string {
	if expanded || !strings.HasPrefix(name, "mcp__") {
		return prettyInput(raw)
	}
	return elidedInput(raw)
}

// elidedInput renders an argument object's keys with each value replaced
// by its type and size. The shape is what tells the user whether to
// approve the call; the contents are what must not be shown for them.
// Numbers, booleans and null are printed as they are — a credential is
// not one of those, and a page number or a flag is worth seeing.
func elidedInput(raw string) string {
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &obj) != nil {
		return "(arguments hidden — ctrl+o to show)"
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "  %q: %s\n", k, elidedValue(obj[k]))
	}
	b.WriteString("}\nvalues hidden — ctrl+o to show")
	return b.String()
}

func elidedValue(v json.RawMessage) string {
	var x any
	if json.Unmarshal(v, &x) != nil {
		return "…"
	}
	switch t := x.(type) {
	case string:
		return fmt.Sprintf("<string, %d chars>", len([]rune(t)))
	case []any:
		return fmt.Sprintf("<array, %d items>", len(t))
	case map[string]any:
		return fmt.Sprintf("<object, %d keys>", len(t))
	case bool, float64, nil:
		return string(v)
	}
	return "…"
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

func clipNote(s string, lines int, expandable bool) string {
	parts := strings.Split(s, "\n")
	if len(parts) <= lines {
		return s
	}
	return strings.Join(parts[:lines], "\n") + "\n" + moreNote(len(parts)-lines, expandable)
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

// previewCall asks a tool what a call would do, for a call whose
// arguments have finished streaming but which has not run yet. The edit
// tools answer with a unified diff against the file on disk; anything
// else, and any error, answers with nothing.
func (m *model) previewCall(name string, input []byte) string {
	if m.opts.Agent == nil || m.opts.Agent.Tools == nil {
		return ""
	}
	t, ok := m.opts.Agent.Tools.Get(name)
	if !ok {
		return ""
	}
	pv, ok := t.(tool.Previewer)
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(m.life, 500*time.Millisecond)
	defer cancel()
	out, err := pv.Preview(ctx, input)
	if err != nil {
		return ""
	}
	return out
}

// toolRenderer finds the tool behind a call when it knows how to present
// itself in the transcript (see tool.Renderer). MCP and user-supplied
// tools do not, and get the generic rendering.
func (m *model) toolRenderer(name string) (tool.Renderer, bool) {
	if m.opts.Agent == nil || m.opts.Agent.Tools == nil {
		return nil, false
	}
	t, ok := m.opts.Agent.Tools.Get(name)
	if !ok {
		return nil, false
	}
	r, ok := t.(tool.Renderer)
	return r, ok
}
