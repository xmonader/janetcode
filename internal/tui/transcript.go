package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
)

type entryKind int

const (
	entryInfo entryKind = iota
	entryError
	entryUser
	entryAssistant
	entryThinking
	entryTool
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

	// render cache
	rendered string
	width    int
	final    bool // rendered from final content (markdown), safe to cache
}

// transcript is the ordered list of things shown in the viewport.
type transcript struct {
	entries   []*entry
	renderer  *glamour.TermRenderer
	rWidth    int
	styleName string // glamour standard style: "dark" or "light"
}

func newTranscript() *transcript { return newTranscriptStyled("dark") }

func newTranscriptStyled(style string) *transcript { return &transcript{styleName: style} }

func (t *transcript) add(e *entry) *entry {
	t.entries = append(t.entries, e)
	return e
}

func (t *transcript) info(s string)   { t.add(&entry{kind: entryInfo, text: s}) }
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
			e.dur = ev.Duration.Round(1e6).String()
		}
	case agent.EvTextBlockDone:
		t.closeOpen()
	case agent.EvTurnDone:
		t.closeOpen()
	case agent.EvCompacted:
		t.closeOpen()
		t.info("⇣ " + ev.Text)
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
// starts a new entry and the finished one gets rendered as markdown.
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

func (t *transcript) render(width int, resized bool, pending *permissionMsg) string {
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
	var sb strings.Builder
	for i, e := range t.entries {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if e.rendered == "" || e.width != width || !e.final || !e.done {
			e.rendered = t.renderEntry(e, width, pending)
			e.width = width
			e.final = e.done
		}
		sb.WriteString(e.rendered)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func (t *transcript) renderEntry(e *entry, width int, pending *permissionMsg) string {
	wrap := lipgloss.NewStyle().Width(width - 2)
	switch e.kind {
	case entryInfo:
		return styleDim.Render(wrap.Render(e.text))
	case entryError:
		return styleErr.Render(wrap.Render("✗ " + e.text))
	case entryUser:
		return styleUser.Render("you") + "\n" + styleUserBody.Render(wrap.Render(e.text))
	case entryThinking:
		txt := strings.TrimSpace(e.text)
		if txt == "" {
			return styleDim.Italic(true).Render("· thinking")
		}
		return styleDim.Italic(true).Render(wrap.Render("· " + txt))
	case entryAssistant:
		if e.done && t.renderer != nil {
			if out, err := t.renderer.Render(e.text); err == nil {
				return strings.TrimRight(out, "\n")
			}
		}
		return wrap.Render(e.text)
	case entryTool:
		return t.renderTool(e, width, pending)
	}
	return e.text
}

func (t *transcript) renderTool(e *entry, width int, pending *permissionMsg) string {
	var sb strings.Builder
	icon := styleTool.Render("⚙")
	switch {
	case e.denied:
		icon = styleErr.Render("⊘")
	case e.done && e.isError:
		icon = styleErr.Render("✗")
	case e.done:
		icon = styleOK.Render("✓")
	case e.running:
		icon = styleAccent.Render("▶")
	}
	sb.WriteString(icon + " " + styleTool.Bold(true).Render(e.call.Name) + " " + styleDim.Render(summarizeInput(e.call.Name, e.inputJSON(), width-len(e.call.Name)-6)))
	if e.dur != "" {
		sb.WriteString(styleDim.Render(" " + e.dur))
	}
	if pending != nil && pending.call.ID == e.call.ID {
		sb.WriteString("\n" + styleWarn.Render("  allow "+e.call.Name+"? [y]es · [a]lways · [n]o"))
		if pending.call.Preview != "" {
			sb.WriteString("\n" + renderPreview(pending.call.Preview, width, 40))
		} else if full := prettyInput(e.inputJSON()); full != "" {
			sb.WriteString("\n" + styleDim.Render(indent(clip(full, 30), "  ")))
		}
		return sb.String()
	}
	if e.done && !e.isError && e.call.Preview != "" && !e.denied {
		sb.WriteString("\n" + renderPreview(e.call.Preview, width, 20))
	}
	if e.done && e.output != "" {
		out := clip(strings.TrimRight(e.output, "\n"), 12)
		style := styleDim
		if e.isError {
			style = styleErr
		}
		sb.WriteString("\n" + style.Render(indent(lipgloss.NewStyle().Width(width-4).Render(out), "  ")))
	}
	return sb.String()
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
			out = append(out, styleDim.Render(fmt.Sprintf("  … (%d more lines)", len(lines)-shown-2)))
			break
		}
		shown++
		if len(l) > width-4 {
			l = l[:width-5] + "…"
		}
		switch {
		case strings.HasPrefix(l, "+"):
			out = append(out, "  "+styleOK.Render(l))
		case strings.HasPrefix(l, "-"):
			out = append(out, "  "+styleErr.Render(l))
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

// summarizeInput shows the most useful argument on one line.
func summarizeInput(name, raw string, max int) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return truncateLine(raw, max)
	}
	for _, k := range []string{"command", "pattern", "path"} {
		if v, ok := m[k].(string); ok {
			extra := ""
			if k == "pattern" {
				if p, ok := m["path"].(string); ok && p != "" {
					extra = " in " + p
				}
			}
			return truncateLine(v+extra, max)
		}
	}
	return truncateLine(raw, max)
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

func truncateLine(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", "⏎ ")
	if max < 10 {
		max = 10
	}
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}

func clip(s string, lines int) string {
	parts := strings.Split(s, "\n")
	if len(parts) <= lines {
		return s
	}
	return strings.Join(parts[:lines], "\n") + fmt.Sprintf("\n… (%d more lines)", len(parts)-lines)
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
