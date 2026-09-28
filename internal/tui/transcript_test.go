package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/flavor"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/tool"
)

// newTranscript is a dark transcript drawn with the default styles.
func newTranscript() *transcript { return newTranscriptStyled("dark", defaultStyles()) }

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func plain(s string) string { return ansiRe.ReplaceAllString(s, "") }

// builtinRenderers is the set of tools that present themselves, for tests
// that render tool results (see tool.Renderer).
func builtinRenderers(name string) (tool.Renderer, bool) {
	switch name {
	case "bash":
		return tool.Bash{}, true
	case "read_file":
		return tool.ReadFile{}, true
	case "edit_file":
		return tool.EditFile{}, true
	case "todo":
		return &agent.TodoTool{}, true
	}
	return nil, false
}

// A tool event with no call names nothing the transcript can draw. It
// must be dropped rather than dereferenced: a malformed event is not a
// reason to take the window down.
func TestToolEventWithoutACallIsDropped(t *testing.T) {
	tr := newTranscript()
	for _, k := range []agent.EventKind{agent.EvToolStart, agent.EvToolRun, agent.EvToolResult} {
		tr.apply(agent.Event{Kind: k, Output: "x"})
	}
	if len(tr.entries) != 0 {
		t.Fatalf("entries = %d, want none", len(tr.entries))
	}
	// A kind that does not name a call is unaffected.
	tr.apply(agent.Event{Kind: agent.EvText, Text: "hello"})
	if len(tr.entries) != 1 {
		t.Fatalf("entries = %d, want the assistant text", len(tr.entries))
	}
}

func TestTranscriptStreamingAndTools(t *testing.T) {
	tr := newTranscript()
	tr.user("read a.txt please")
	call := &agent.ToolCall{ID: "t1", Name: "read_file"}
	tr.apply(agent.Event{Kind: agent.EvThinking, Text: "let me "})
	tr.apply(agent.Event{Kind: agent.EvThinking, Text: "look"})
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: call})
	tr.apply(agent.Event{Kind: agent.EvToolInput, Text: `{"path":`})
	tr.apply(agent.Event{Kind: agent.EvToolInput, Text: `"a.txt"}`})
	tr.apply(agent.Event{Kind: agent.EvTextBlockDone})
	full := &agent.ToolCall{ID: "t1", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)}
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: full})
	out := plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
	if !strings.Contains(out, "▶ read_file a.txt") {
		t.Fatalf("running tool not shown:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: full, Output: "1\talpha\n2\tbeta", Duration: 3 * time.Millisecond})
	tr.apply(agent.Event{Kind: agent.EvText, Text: "It says **alpha**"})
	tr.apply(agent.Event{Kind: agent.EvText, Text: " and beta."})
	tr.apply(agent.Event{Kind: agent.EvTextBlockDone})
	tr.apply(agent.Event{Kind: agent.EvTurnDone})
	out = plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
	for _, want := range []string{"❯ read a.txt please", "· thought: let me look", "✓ read_file a.txt", "alpha", "beta", "and beta."} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "**alpha**") {
		t.Errorf("markdown not rendered:\n%s", out)
	}
	// A new text delta after the turn starts a fresh assistant entry.
	n := len(tr.entries)
	tr.apply(agent.Event{Kind: agent.EvText, Text: "next"})
	if len(tr.entries) != n+1 {
		t.Fatalf("expected new entry, entries=%d", len(tr.entries))
	}
}

func TestTranscriptPermissionAndErrors(t *testing.T) {
	tr := newTranscript()
	call := agent.ToolCall{ID: "t9", Name: "bash", Input: json.RawMessage(`{"command":"rm -rf build"}`)}
	edit := agent.ToolCall{ID: "t8", Name: "edit_file", Input: json.RawMessage(`{"path":"f"}`), Preview: "--- f\n+++ f\n@@ -1,1 +1,1 @@\n-old line\n+new line\n"}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &edit})
	// A changed line's tint runs to the edge; the text is what is checked.
	if out := trimLineEnds(plain(tr.render(renderOpts{width: 80, pending: &permissionMsg{call: edit}}))); !strings.Contains(out, "-old line\n") || !strings.Contains(out, "+new line") || strings.Contains(out, "+++ f") {
		t.Fatalf("diff preview missing:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &edit, Output: "Edited f"})
	if out := plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers})); !strings.Contains(out, "+new line") {
		t.Fatalf("diff should stay visible after completion:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &call})
	call.Warning = "recursive force delete"
	pending := &permissionMsg{call: call}
	out := plain(tr.render(renderOpts{width: 80, pending: pending}))
	if !strings.Contains(out, "⚙ bash rm -rf build") {
		t.Fatalf("permission prompt missing:\n%s", out)
	}
	tr.permissionAnswered(call, agent.Deny)
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: "denied", IsError: true})
	out = plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
	if !strings.Contains(out, "⊘ bash rm -rf build") {
		t.Fatalf("denied marker missing:\n%s", out)
	}
	tr.errorf("boom %d", 7)
	if out = plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers})); !strings.Contains(out, "✗ boom 7") {
		t.Fatalf("error missing:\n%s", out)
	}
}

func TestLoadHistory(t *testing.T) {
	tr := newTranscript()
	tr.loadHistory([]llm.Message{
		llm.TextMessage(llm.RoleUser, "hi"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "a", Name: "glob", Input: json.RawMessage(`{"pattern":"*.go"}`)}}},
		llm.ToolResultMessage(llm.ToolResult("a", "main.go", false)),
		llm.TextMessage(llm.RoleAssistant, "one file"),
	})
	out := plain(tr.render(renderOpts{width: 60}))
	for _, want := range []string{"❯ hi", "✓ glob *.go", "main.go", "one file"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSummarizeInput(t *testing.T) {
	if got := present.ToolSummary("grep", `{"pattern":"foo","path":"src"}`, 40, ""); got != "foo in src" {
		t.Fatalf("got %q", got)
	}
	if got := present.ToolSummary("bash", `{"command":"a\nb"}`, 40, ""); got != "a⏎ b" {
		t.Fatalf("got %q", got)
	}
	if got := present.ToolSummary("x", `{"k":"`+strings.Repeat("v", 50)+`"}`, 20, ""); len(got) > 22 || !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q", got)
	}
}

// An MCP tool's argument values often carry credentials copied from the
// context; the permission line names the keys only, and never a value.
func TestSummarizeInputMCPKeysOnly(t *testing.T) {
	secret := "sk-secret-token-value"
	got := present.ToolSummary("mcp__deploy__ship", `{"repo":"a/b","api_key":"`+secret+`","dry_run":true}`, 200, "")
	if got != "{api_key, dry_run, repo}" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, secret) {
		t.Fatal("a value reached the permission line")
	}
	if got := present.ToolSummary("mcp__x__y", `["`+secret+`"]`, 200, ""); strings.Contains(got, secret) || got != "(json array)" {
		t.Fatalf("got %q", got)
	}
	if got := present.ToolSummary("mcp__x__y", "not json at all: "+secret, 200, ""); strings.Contains(got, secret) || got != "(arguments are not valid JSON)" {
		t.Fatalf("got %q", got)
	}
	// Truncation keeps long key lists on one line.
	many := `{"` + strings.Repeat("k", 60) + `":"v","b":"v","c":"v"}`
	if got := present.ToolSummary("mcp__x__y", many, 20, ""); len([]rune(got)) > 20 {
		t.Fatalf("got %d runes: %q", len([]rune(got)), got)
	}
}

func TestThinkingView(t *testing.T) {
	got := thinkingView("", false, 0, time.Unix(1_700_000_000, 0))
	if !strings.HasPrefix(got, "· ") || !flavor.Known(strings.TrimPrefix(got, "· ")) {
		t.Fatalf("empty live: %q", got)
	}
	if got := thinkingView("", true, 0, time.Time{}); got != "· thinking" {
		t.Fatalf("empty done: %q", got)
	}
	long := strings.Repeat("line\n", 20) + "last"
	got = thinkingView(long, false, 0, time.Time{})
	if !strings.HasPrefix(got, "· thinking… (19 earlier lines)") || !strings.HasSuffix(got, "last") || strings.Count(got, "\n") != 2 {
		t.Fatalf("streaming tail: %q", got)
	}
	if got := thinkingView("plan first\nthen do", true, 0, time.Time{}); got != "· thought: plan first (2 lines)" {
		t.Fatalf("done: %q", got)
	}
}

func TestTruncateLineIsWidthAware(t *testing.T) {
	colored := "\x1b[1mhello\x1b[0m world"
	if got := truncateLine(colored, 20); got != colored {
		t.Fatalf("colour codes counted as width: %q", got)
	}
	if got := truncateLine("ééééééééééééééé", 10); got != "ééééééééé…" {
		t.Fatalf("rune split: %q", got)
	}
}

// TestPathsAndThoughtsFit: tool lines show paths relative to the working
// directory so the file name survives truncation; a folded thought ends
// at a word, not mid-word.
func TestPathsAndThoughtsFit(t *testing.T) {
	if got := present.ToolSummary("read_file", `{"path":"/work/proj/internal/x/x.go","offset":120}`, 60, "/work/proj"); got != "internal/x/x.go from line 120" {
		t.Fatalf("got %q", got)
	}
	if got := present.ToolSummary("grep", `{"pattern":"foo","path":"/work/proj/src"}`, 60, "/work/proj"); got != "foo in src" {
		t.Fatalf("got %q", got)
	}
	if got := relTo("/work/proj", "/elsewhere/a.go"); got != "/elsewhere/a.go" {
		t.Fatalf("outside the tree stays absolute: %q", got)
	}
	if got := relTo("/work/proj", "rel/b.go"); got != "rel/b.go" {
		t.Fatalf("relative stays: %q", got)
	}
	long := strings.Repeat("word ", 30)
	got := cutWords(long, 40)
	if len(got) > 41 || !strings.HasSuffix(got, "…") || strings.HasSuffix(strings.TrimSuffix(got, "…"), "wor") {
		t.Fatalf("cut mid-word: %q", got)
	}
	if got := cutWords("short", 40); got != "short" {
		t.Fatalf("short text changed: %q", got)
	}
}

// TestVerdictAndDiagnosticsAreShown: the verdict event becomes a line of
// its own with its mark; language-server diagnostics appended to a tool
// result are split out and never clipped away.
func TestVerdictAndDiagnosticsAreShown(t *testing.T) {
	tr := newTranscript()
	tr.apply(agent.Event{Kind: agent.EvVerdict, Text: "⚠ not demonstrated: 1 file(s) edited and nothing asserted the result", IsError: true})
	if e := tr.last(entryVerdict); e == nil || !strings.HasPrefix(e.text, "⚠ not demonstrated") {
		t.Fatalf("verdict entry = %+v", e)
	}
	if got := plain(tr.renderEntry(tr.last(entryVerdict), 80, renderOpts{})); !strings.Contains(got, "not demonstrated") {
		t.Fatalf("rendered %q", got)
	}
	body, diag := splitDiagnostics("Edited a.go (1 replacement)\n[diagnostics]\na.go:3:5: undefined: x\na.go:9:1: missing return")
	if body != "Edited a.go (1 replacement)" || !strings.HasPrefix(diag, "✗ a.go:3:5") || !strings.Contains(diag, "missing return") {
		t.Fatalf("split = %q / %q", body, diag)
	}
	if b, d := splitDiagnostics("plain output"); b != "plain output" || d != "" {
		t.Fatalf("no diagnostics: %q / %q", b, d)
	}
	tr.apply(agent.Event{Kind: agent.EvNotice, Text: "skills attached: go-testing"})
	if e := tr.last(entryInfo); e == nil || !strings.HasPrefix(e.text, "» ") {
		t.Fatalf("attach note = %+v", e)
	}
	if verdictBrief("✓ verified: tests that had been failing now pass") != "✓ verified" || verdictBrief("") != "" {
		t.Fatal("verdictBrief")
	}
}

// TestStreamingWriteShowsProgress: while a write_file's input streams,
// the line names the file and how much has arrived, instead of a raw
// JSON prefix and "preparing write_file" for a minute.
func TestStreamingWriteShowsProgress(t *testing.T) {
	partial := `{"path": "/work/proj/scheme.py", "content": "#!/usr/bin/env python3\nimport sys\n\ndef main():\n    pass\n`
	got := streamingSummary("write_file", partial, "/work/proj")
	if !strings.HasPrefix(got, "scheme.py · writing 6 lines") || !strings.Contains(got, "so far") {
		t.Fatalf("got %q", got)
	}
	if got := streamingSummary("write_file", `{"pa`, "/work/proj"); got != "…" {
		t.Fatalf("before the path: %q", got)
	}
	if got := streamingSummary("bash", `{"command": "go test ./... && echo o`, ""); !strings.HasPrefix(got, "go test ./... && echo o") || !strings.HasSuffix(got, "…") {
		t.Fatalf("bash: %q", got)
	}
	if got := streamingSummary("read_file", `{"path":"a"}`, ""); got != "" {
		t.Fatalf("other tools keep the normal summary: %q", got)
	}
}

// TestPlanUpdatesRenderAsTheToolWroteThem: the delta is the tool's own
// answer now, so the transcript shows the result as it stands rather
// than deltaing it a second time — which would drop the lines the two
// deltas happen to share.
func TestPlanUpdatesRenderAsTheToolWroteThem(t *testing.T) {
	first := "[ ] design\n[ ] parse\n[ ] eval\n\n0 of 3 done"
	update := "[>] design\n0 of 3 done"
	tr := newTranscript()
	c1 := agent.ToolCall{ID: "1", Name: "todo"}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &c1})
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c1})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c1, Output: first})
	c2 := agent.ToolCall{ID: "2", Name: "todo"}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &c2})
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c2})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c2, Output: update})
	out := plain(tr.renderTool(tr.findTool("2"), 80, renderOpts{renderer: builtinRenderers}))
	if strings.Contains(out, "[ ] parse") || !strings.Contains(out, "[>] design") || !strings.Contains(out, "0 of 3 done") {
		t.Fatalf("the update was not shown as the tool wrote it:\n%s", out)
	}
	whole := plain(tr.renderTool(tr.findTool("1"), 80, renderOpts{renderer: builtinRenderers}))
	if !strings.Contains(whole, "[ ] parse") {
		t.Fatalf("the first plan was not shown whole:\n%s", whole)
	}
}

// TestGoalEndIsALineOfItsOwn: the goal_status result is what the user is
// waiting for, so it is shown as a marked line, not a faint tool result.
func TestGoalEndIsALineOfItsOwn(t *testing.T) {
	tr := newTranscript()
	c := agent.ToolCall{ID: "g", Name: "goal_status"}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &c})
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c, Output: "goal g1 is now complete"})
	e := tr.last(entryVerdict)
	if e == nil || !strings.Contains(e.text, "◎ goal g1 is now complete") {
		t.Fatalf("goal end entry = %+v", e)
	}
}

// TestMarkdownStyleHasNoBackgrounds pins item 1 of the polish round:
// glamour's standard styles paint a background behind inline code
// (48;5;236 dark, 48;5;254 light) and behind a code block, which reads as
// a grey smear on any other terminal background.
func TestMarkdownStyleHasNoBackgrounds(t *testing.T) {
	const src = "# Heading\n\nSome `inline code` and *emph* here.\n\n```go\nfunc main() {}\n```\n"
	for _, style := range []string{"dark", "light"} {
		tr := newTranscriptStyled(style, defaultStyles())
		tr.apply(agent.Event{Kind: agent.EvText, Text: src})
		tr.apply(agent.Event{Kind: agent.EvTurnDone})
		out := tr.render(renderOpts{width: 80, renderer: builtinRenderers})
		if strings.Contains(out, "\x1b[48;") || strings.Contains(out, ";48;") {
			t.Errorf("%s style still sets a background:\n%q", style, out)
		}
		if strings.Contains(out, ";3m") || strings.Contains(out, "[3m") {
			t.Errorf("%s style still emits italics:\n%q", style, out)
		}
		for _, want := range []string{"Heading", "inline code", "func main"} {
			if !strings.Contains(plain(out), want) {
				t.Errorf("%s style lost %q:\n%s", style, want, plain(out))
			}
		}
	}
}

// TestMarkdownStyleUsesPalette checks the style is built from styles.go
// rather than glamour's own colours.
func TestMarkdownStyleUsesPalette(t *testing.T) {
	for _, dark := range []bool{true, false} {
		p := defaultStyles().col
		c := markdownStyle(p, dark)
		accent, tool := p.accent.Light, p.tool.Light
		if dark {
			accent, tool = p.accent.Dark, p.tool.Dark
		}
		if c.Heading.Color == nil || *c.Heading.Color != accent {
			t.Errorf("dark=%v heading colour = %v, want %s", dark, c.Heading.Color, accent)
		}
		if c.H1.Color == nil || *c.H1.Color != accent {
			t.Errorf("dark=%v h1 colour = %v, want %s", dark, c.H1.Color, accent)
		}
		if c.Code.Color == nil || *c.Code.Color != tool {
			t.Errorf("dark=%v code colour = %v, want %s", dark, c.Code.Color, tool)
		}
		for name, bg := range map[string]*string{
			"h1": c.H1.BackgroundColor, "heading": c.Heading.BackgroundColor,
			"code": c.Code.BackgroundColor, "code_block": c.CodeBlock.BackgroundColor,
		} {
			if bg != nil {
				t.Errorf("dark=%v %s keeps a background %q", dark, name, *bg)
			}
		}
		if c.CodeBlock.Chroma == nil || c.CodeBlock.Chroma.Background.BackgroundColor != nil {
			t.Errorf("dark=%v chroma canvas still has a background", dark)
		}
		if c.Emph.Italic == nil || *c.Emph.Italic {
			t.Errorf("dark=%v emph is still italic", dark)
		}
	}
}

// TestThinkingViewCapsAtThreeLines covers item 3: a streaming thought used
// to show its last eight lines and then fold to one, so the transcript
// jumped seven lines every time the model moved on.
func TestThinkingViewCapsAtThreeLines(t *testing.T) {
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, fmt.Sprintf("step %d", i))
	}
	text := strings.Join(lines, "\n")
	live := thinkingView(text, false, 0, time.Time{})
	if n := len(strings.Split(live, "\n")); n != 3 {
		t.Fatalf("streaming thought is %d lines, want 3:\n%s", n, live)
	}
	if !strings.Contains(live, "step 12") || !strings.Contains(live, "step 11") {
		t.Errorf("streaming thought lost the tail:\n%s", live)
	}
	if !strings.Contains(live, "10 earlier lines") {
		t.Errorf("streaming thought does not say what it hid:\n%s", live)
	}
	folded := thinkingView(text, true, 0, time.Time{})
	if n := len(strings.Split(folded, "\n")); n != 1 {
		t.Fatalf("folded thought is %d lines, want 1:\n%s", n, folded)
	}
	if !strings.Contains(folded, "step 1") || !strings.Contains(folded, "12 lines") {
		t.Errorf("folded thought = %q", folded)
	}
	// Short thoughts are shown whole.
	if got := thinkingView("only one", false, 0, time.Time{}); got != "· only one" {
		t.Errorf("short thought = %q", got)
	}
}

// TestToolResultCollapsing covers item 4: results collapsed at four lines,
// so "… (2 more lines, ctrl+o to expand)" appeared to save two lines;
// read_file printed the file back at the reader; and edit_file printed the
// diff, the summary and then a numbered window of the same change.
func TestToolResultCollapsing(t *testing.T) {
	read := agent.ToolCall{ID: "r1", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)}
	body := ""
	for i := 1; i <= 6; i++ {
		body += fmt.Sprintf("%d\tline %d\n", i, i)
	}

	// Six lines of any other tool's output are shown whole now.
	other := agent.ToolCall{ID: "b1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}
	tr := newTranscript()
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &other})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &other, Output: body})
	out := plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
	if strings.Contains(out, "more lines") {
		t.Errorf("six lines were collapsed:\n%s", out)
	}
	// Twelve are not: that hides four.
	long := body
	for i := 7; i <= 12; i++ {
		long += fmt.Sprintf("%d\tline %d\n", i, i)
	}
	tr = newTranscript()
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &other})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &other, Output: long})
	out = plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
	// Command output keeps its tail: two lines of head, three of tail.
	if !strings.Contains(out, "… +7 lines") || !strings.Contains(out, "line 12") || strings.Contains(out, "line 6") {
		t.Errorf("twelve lines were not folded head 2 / tail 3:\n%s", out)
	}

	// read_file folds to one line, and expands again with ctrl+o.
	tr = newTranscript()
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &read})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &read, Output: body + "... (36 more lines)\n"})
	out = plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
	if !strings.Contains(out, "read main.go: 6 of 42 lines") {
		t.Errorf("read_file not folded:\n%s", out)
	}
	if strings.Contains(out, "line 3") {
		t.Errorf("read_file still prints the file:\n%s", out)
	}
	if out = plain(tr.render(renderOpts{width: 80, verbose: true, renderer: builtinRenderers})); !strings.Contains(out, "line 3") {
		t.Errorf("ctrl+o does not bring the lines back:\n%s", out)
	}

	// edit_file keeps the diff, the summary and the diagnostics, and drops
	// the numbered window of the file.
	edit := agent.ToolCall{ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"a.go"}`),
		Preview: "--- a.go\n+++ a.go\n@@ -1,2 +1,2 @@\n-old\n+new\n"}
	tr = newTranscript()
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &edit})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &edit,
		Output: "Edited a.go (1 replacement, +1 -1 lines)\n\n     1\tpackage main\n     2\tnew\n[diagnostics]\na.go:2:1: undefined: new"})
	out = plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
	for _, want := range []string{"+new", "-old", "Edited a.go (1 replacement, +1 -1 lines)", "a.go:2:1: undefined: new"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "package main") {
		t.Errorf("the numbered window is still shown:\n%s", out)
	}
}

// TestNoticeVocabulary covers item 8: every line that arrives on its own
// carries one glyph from the small vocabulary documented in transcript.go,
// and a tip is rendered fainter than an ordinary notice.
func TestNoticeVocabulary(t *testing.T) {
	tr := newTranscript()
	tr.apply(agent.Event{Kind: agent.EvNotice, Text: "skills attached: release"})
	tr.apply(agent.Event{Kind: agent.EvNotice, Text: "verification gate failed"})
	tr.apply(agent.Event{Kind: agent.EvCompacted, Text: "compacted 40k tokens"})
	tr.tip("/verify set \"go test ./...\" runs the project's checks")
	out := tr.render(renderOpts{width: 80, renderer: builtinRenderers})
	flat := plain(out)
	for _, want := range []string{
		noteAttached + "skills attached: release",
		noteNotice + "verification gate failed",
		noteCompact + "compacted 40k tokens",
		"tip · /verify set",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("missing %q in:\n%s", want, flat)
		}
	}
	// No line ends in a full stop, and none starts with two glyphs.
	for _, l := range strings.Split(strings.TrimRight(flat, "\n"), "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), ".") {
			t.Errorf("notice ends with a full stop: %q", l)
		}
	}
	// The tip is faint where a notice is dim: the two must differ.
	tipOnly := newTranscript()
	tipOnly.tip("x")
	infoOnly := newTranscript()
	infoOnly.info("x")
	if tipOnly.render(renderOpts{width: 80, renderer: builtinRenderers}) == infoOnly.render(renderOpts{width: 80, renderer: builtinRenderers}) {
		t.Error("a tip is styled the same as an ordinary notice")
	}
}

// TestPendingCallIsNotStillStreaming: once a call waits for permission
// its input is complete, so the line shows the call, not "editing N
// lines so far".
func TestPendingCallIsNotStillStreaming(t *testing.T) {
	tr := newTranscript()
	c := agent.ToolCall{ID: "e", Name: "edit_file"}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &c})
	tr.apply(agent.Event{Kind: agent.EvToolInput, Text: `{"path":"main.go","edits":[{"old_string":"a","new_string":"b"}]}`})
	e := tr.findTool("e")
	if got := plain(tr.renderTool(e, 80, renderOpts{})); !strings.Contains(got, "so far") {
		t.Fatalf("while streaming: %q", got)
	}
	full := agent.ToolCall{ID: "e", Name: "edit_file", Input: []byte(`{"path":"main.go","edits":[{"old_string":"a","new_string":"b"}]}`)}
	got := plain(tr.renderTool(e, 80, renderOpts{pending: &permissionMsg{call: full}}))
	if strings.Contains(got, "so far") || !strings.Contains(got, "main.go") {
		t.Fatalf("while pending: %q", got)
	}
}

// TestPreviewTruncationKeepsUTF8: a diff line wider than the terminal used
// to be cut with a byte slice, which split a multi-byte rune in half and
// printed a replacement character.
func TestPreviewTruncationKeepsUTF8(t *testing.T) {
	preview := "--- a.txt\n+++ a.txt\n@@ -1 +1 @@\n+" + strings.Repeat("é", 60) + "\n"
	out := plain(renderPreview(defaultStyles(), preview, 40, 10, true))
	if strings.ContainsRune(out, '�') || !utf8.ValidString(out) {
		t.Fatalf("truncation corrupted UTF-8: %q", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if lipgloss.Width(l) > 40 {
			t.Fatalf("line %d columns wide, want <= 40: %q", lipgloss.Width(l), l)
		}
	}
	if !strings.Contains(out, "é") || !strings.Contains(out, "…") {
		t.Fatalf("expected an elided run of é: %q", out)
	}
}

// TestExpandLastTurnOnly: ctrl+o reopens the entries of the most recent
// turn and folds them again; an older turn keeps its folded form, which is
// what /verbose is for.
func TestExpandLastTurnOnly(t *testing.T) {
	tr := newTranscript()
	long := strings.Repeat("line\n", 40)
	add := func(prompt string) {
		tr.user(prompt)
		call := agent.ToolCall{ID: prompt, Name: "bash"}
		tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
		tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: long})
	}
	add("first")
	add("second")
	o := renderOpts{width: 80, renderer: builtinRenderers}
	count := func() int { return strings.Count(plain(tr.render(o)), "line") }
	folded := count()
	expanded, any := tr.toggleLastTurn()
	if !expanded || !any {
		t.Fatalf("toggleLastTurn = %v %v", expanded, any)
	}
	after := count()
	if after <= folded {
		t.Fatalf("expanding showed no more lines: %d then %d", folded, after)
	}
	if tr.entries[0].expanded || !tr.entries[len(tr.entries)-1].expanded {
		t.Fatal("expansion should cover the last turn only")
	}
	if expanded, _ := tr.toggleLastTurn(); expanded {
		t.Fatal("a second ctrl+o should fold")
	}
	if count() != folded {
		t.Fatalf("folding back gave %d lines, want %d", count(), folded)
	}
}

// TestClickTogglesOneEntry: the line range recorded during rendering maps
// a click back to the entry drawn there.
func TestClickTogglesOneEntry(t *testing.T) {
	tr := newTranscript()
	tr.user("go")
	call := agent.ToolCall{ID: "b", Name: "bash"}
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: strings.Repeat("x\n", 40)})
	tr.render(renderOpts{width: 80, renderer: builtinRenderers})
	last := tr.entries[len(tr.entries)-1]
	if got := tr.entryAtLine(last.lineFrom); got != last {
		t.Fatalf("entryAtLine(%d) found the wrong entry", last.lineFrom)
	}
	if tr.entryAtLine(tr.entries[0].lineFrom) != nil {
		t.Fatal("a user line is not expandable")
	}
}

// TestClickFindsTheEntryUnderTheStickyPlan: the click was mapped as if
// the transcript always started on the row under the header, so with the
// plan strip up every click landed on the entry that many lines further
// down — or on none — and a click over a dialog, where no transcript is
// drawn at all, still toggled whatever entry was hidden under it.
func TestClickFindsTheEntryUnderTheStickyPlan(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.WriteTodos([]agent.Todo{
		{Text: "explore the parser", Status: agent.TodoDone},
		{Text: "write the parser", Status: agent.TodoDoing},
		{Text: "add tests", Status: agent.TodoPending},
	})
	call := agent.ToolCall{ID: "b", Name: "bash", Input: json.RawMessage(`{"command":"echo clickme"}`)}
	h.m.transcript.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	h.m.transcript.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: strings.Repeat("x\n", 40)})
	h.m.layout()
	h.m.refresh(false)
	if h.m.planHeight() == 0 {
		t.Fatal("the plan strip is not up")
	}
	row := -1
	for i, l := range strings.Split(plain(h.m.View()), "\n") {
		if strings.Contains(l, "echo clickme") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("the tool line is not on screen:\n%s", plain(h.m.View()))
	}
	e := h.m.transcript.findTool("b")
	h.m.openOverlay(overlayTodos)
	if h.m.clickTranscript(row) || e.expanded {
		t.Fatal("a click over a dialog toggled the transcript entry hidden under it")
	}
	h.m.closeOverlay()
	// The row above the tool line is the gap before it: nothing to open.
	if h.m.clickTranscript(row-1) || e.expanded {
		t.Fatalf("a click on row %d, above the tool line, opened it", row-1)
	}
	if !h.m.clickTranscript(row) || !e.expanded {
		t.Fatalf("a click on row %d did not open the tool line drawn there", row)
	}
}

// TestFoldedThoughtReopens: a thought folded to one line can be read again.
func TestFoldedThoughtReopens(t *testing.T) {
	tr := newTranscript()
	tr.apply(agent.Event{Kind: agent.EvThinking, Text: "one\ntwo\nthree\nfour"})
	tr.apply(agent.Event{Kind: agent.EvTextBlockDone})
	o := renderOpts{width: 80, renderer: builtinRenderers}
	if v := plain(tr.render(o)); strings.Contains(v, "four") {
		t.Fatalf("a finished thought should fold:\n%s", v)
	}
	tr.toggleLastTurn()
	if v := plain(tr.render(o)); !strings.Contains(v, "four") {
		t.Fatalf("expanded thought should show its text:\n%s", v)
	}
}

// TestFailedCommandKeepsItsTail: the reason a build failed is on its last
// line, so a clipped error must keep the end and not the preamble.
func TestFailedCommandKeepsItsTail(t *testing.T) {
	call := agent.ToolCall{ID: "b", Name: "bash", Input: json.RawMessage(`{"command":"go build ./..."}`)}
	body := ""
	for i := 1; i <= 30; i++ {
		body += fmt.Sprintf("noise %d\n", i)
	}
	body += "main.go:9:2: undefined: Sub"
	tr := newTranscript()
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: body, IsError: true})
	out := plain(tr.render(renderOpts{width: 100, renderer: builtinRenderers}))
	if !strings.Contains(out, "undefined: Sub") {
		t.Fatalf("the error line was clipped away:\n%s", out)
	}
	if strings.Contains(out, "noise 1\n") {
		t.Fatalf("the preamble survived instead:\n%s", out)
	}
	if !strings.Contains(out, "… +") {
		t.Fatalf("no marker for what was dropped:\n%s", out)
	}
}

// TestPreviewBeforeTheCallRuns: once an edit's arguments have finished
// streaming, the diff against the file on disk is shown while the model
// is still writing the rest of the turn.
func TestPreviewBeforeTheCallRuns(t *testing.T) {
	asked := 0
	o := renderOpts{width: 80, preview: func(name string, input []byte) string {
		asked++
		return "--- a.go\n+++ a.go\n@@ -1,1 +1,1 @@\n-old line\n+new line\n"
	}}
	tr := newTranscript()
	call := agent.ToolCall{ID: "e1", Name: "edit_file"}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &call})
	tr.apply(agent.Event{Kind: agent.EvToolInput, Call: &call, Text: `{"path":"a.go",`})
	if v := plain(tr.render(o)); strings.Contains(v, "new line") {
		t.Fatalf("a half-written call must not be previewed:\n%s", v)
	}
	tr.apply(agent.Event{Kind: agent.EvToolInput, Call: &call, Text: `"old_string":"old line","new_string":"new line"}`})
	v := plain(tr.render(o))
	if !strings.Contains(v, "+new line") || !strings.Contains(v, "-old line") {
		t.Fatalf("no diff before the call ran:\n%s", v)
	}
	// Re-rendering the same call does not recompute it.
	before := asked
	tr.render(o)
	tr.render(o)
	if asked != before {
		t.Fatalf("preview recomputed %d times", asked-before+1)
	}
}

// TestToolOwnedRenderingWithFallback: a tool that says how its result
// reads is obeyed; one that says nothing — every MCP tool — gets the
// generic fold rather than a special case named after it.
func TestToolOwnedRenderingWithFallback(t *testing.T) {
	long := strings.Repeat("x\n", 30)
	render := func(name string, r func(string) (tool.Renderer, bool)) string {
		tr := newTranscript()
		c := agent.ToolCall{ID: "1", Name: name}
		tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c})
		tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c, Output: long})
		return plain(tr.renderTool(tr.findTool("1"), 80, renderOpts{width: 80, renderer: r}))
	}
	// bash renders itself: head two, tail three.
	if got := render("bash", builtinRenderers); !strings.Contains(got, "… +25 lines") {
		t.Fatalf("bash did not render itself:\n%s", got)
	}
	// An MCP tool has no renderer and folds generically, from the head.
	got := render("mcp__srv__thing", builtinRenderers)
	if !strings.Contains(got, "more lines, ctrl+o to expand") {
		t.Fatalf("no fallback fold for an MCP tool:\n%s", got)
	}
	if strings.Contains(got, "… +25 lines") {
		t.Fatalf("the MCP tool got bash's rendering:\n%s", got)
	}
	// With no renderer at all — a headless render — the fallback holds.
	if got := render("bash", nil); !strings.Contains(got, "more lines, ctrl+o to expand") {
		t.Fatalf("no fallback without a registry:\n%s", got)
	}
}

// TestFinishedCallFlashesAndThoughtSaysHowLong: a call that has just
// finished is drawn brightly for a moment, and a folded thought says how
// long the model spent on it.
func TestFinishedCallFlashesAndThoughtSaysHowLong(t *testing.T) {
	tr := newTranscript()
	c := agent.ToolCall{ID: "1", Name: "bash"}
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c, Output: "ok"})
	e := tr.findTool("1")
	if e.ended.IsZero() {
		t.Fatal("a finished call must record when it finished")
	}
	// The flash is a colour, so the test needs a terminal that has them.
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)
	fresh := tr.renderTool(e, 80, renderOpts{width: 80})
	e.ended = time.Now().Add(-time.Second) // past the flash
	settled := tr.renderTool(e, 80, renderOpts{width: 80})
	if fresh == settled {
		t.Fatal("a call that has just finished should be drawn differently")
	}
	if !strings.Contains(plain(fresh), "✓") || !strings.Contains(plain(settled), "✓") {
		t.Fatal("the mark itself must not change")
	}

	th := &entry{kind: entryThinking, text: "a\nb", done: true,
		started: time.Now().Add(-12300 * time.Millisecond), ended: time.Now()}
	if got := thinkingView(th.text, true, th.thoughtFor(), th.started); !strings.Contains(got, "thought for 12.3s") {
		t.Fatalf("thought header = %q", got)
	}
	// A resumed conversation has no timings, and a thought too short to
	// have been waited for says nothing about how long it took.
	for _, d := range []time.Duration{0, 40 * time.Millisecond} {
		if got := thinkingView("a", true, d, time.Time{}); !strings.HasPrefix(got, "· thought: ") {
			t.Fatalf("thought after %s = %q", d, got)
		}
	}
}

// TestControlCharactersNeverReachThePrompt: a file the model was asked to
// look at could carry an escape sequence into the approval prompt, where
// it could repaint the box and hide what is being approved.
func TestControlCharactersNeverReachThePrompt(t *testing.T) {
	nasty := "rm -rf /" + "\x1b[2K\x1b[A" + "echo safe" + "\a" + "more"
	got := sanitize(nasty)
	for _, r := range got {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("control character %q survived: %q", r, got)
		}
	}
	if !strings.Contains(got, "rm -rf /") || !strings.Contains(got, "echo safe") {
		t.Fatalf("the text itself was lost: %q", got)
	}
	if sanitize("a\tb") != "a b" {
		t.Fatalf("a tab should become a space, got %q", sanitize("a\tb"))
	}
}

// TestResumeReplaysTerminalStatesOnly: a call whose result never reached
// the session must not come back looking as though it succeeded.
func TestResumeReplaysTerminalStatesOnly(t *testing.T) {
	tr := newTranscript()
	tr.loadHistory([]llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "go"}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "a", Name: "bash", Input: []byte(`{"command":"true"}`)},
			{Type: llm.BlockToolUse, ID: "b", Name: "bash", Input: []byte(`{"command":"hang"}`)},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "a", Text: "ok"}}},
	})
	done, hung := tr.findTool("a"), tr.findTool("b")
	if done.isError {
		t.Fatal("a call with a result is not an error")
	}
	if !hung.isError || !strings.Contains(hung.output, "session ended") {
		t.Fatalf("a call with no result replayed as %v %q", hung.isError, hung.output)
	}
	if v := plain(tr.render(renderOpts{width: 80})); !strings.Contains(v, "\u2717") {
		t.Fatalf("the unfinished call is not marked:\n%s", v)
	}
}

// A command that redirects into the project carried the whole absolute
// path, which is the same for every call and ate the width the command
// itself needed: the prompt read "allow bash seq 1 400 >
// /tmp/claude-1000/-home-driver-wspace-…" and never reached the file
// name, which is the one thing worth approving.
func TestCommandSummaryShortensPaths(t *testing.T) {
	cwd := "/tmp/claude-1000/-home-driver/scratch/proj"
	raw := `{"command":"seq 1 400 > ` + cwd + `/numbers.txt"}`
	got := present.ToolSummary("bash", raw, 80, cwd)
	if strings.Contains(got, cwd) {
		t.Fatalf("absolute path kept: %q", got)
	}
	if got != "seq 1 400 > numbers.txt" {
		t.Fatalf("summary = %q", got)
	}
	// Paths elsewhere still shorten to ~ where they can, and a command
	// with no path in it is untouched.
	if got := present.ToolSummary("bash", `{"command":"go test ./..."}`, 80, cwd); got != "go test ./..." {
		t.Fatalf("summary = %q", got)
	}
}

// TestListItemSoftBreaksReflow: glamour keeps a soft line break inside a
// list item as a hard one, so a model's source line break came out as a
// ragged paragraph ("…multi-byte characters" / "when truncating…") in a
// window wide enough for both. Soft breaks inside a list item now reflow
// like a paragraph's; a new item, a nested list and a hard break stay.
func TestListItemSoftBreaksReflow(t *testing.T) {
	text := "- **Clip** now counts display width, so it no longer splits multi-byte characters\n  when truncating long lines.\n  - nested item\n- second item ends in a hard break  \n  kept on its own line"
	tr := newTranscriptStyled("dark", defaultStyles())
	tr.apply(agent.Event{Kind: agent.EvText, Text: text})
	tr.apply(agent.Event{Kind: agent.EvTurnDone})
	out := plain(tr.render(renderOpts{width: 140}))
	if !strings.Contains(out, "multi-byte characters when truncating long lines.") {
		t.Fatalf("the soft break was kept:\n%s", out)
	}
	for _, want := range []string{"nested item", "second item ends in a hard break"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "hard break") && strings.Contains(l, "kept on its own line") {
			t.Fatalf("a hard break was joined:\n%s", out)
		}
		if strings.Contains(l, "long lines.") && strings.Contains(l, "nested") {
			t.Fatalf("the nested list was joined:\n%s", out)
		}
	}
}

// TestWrappedListItemsAndQuotesHang: glamour wrapped an item's second line
// back under its bullet and dropped the bar from a wrapped quote line. A
// wrapped item now continues under its own text, a numbered one under the
// words after the number, and every line of a quote keeps its bar, while
// the gap between two quoted paragraphs stays.
func TestWrappedListItemsAndQuotesHang(t *testing.T) {
	render := func(text string) []string {
		tr := newTranscriptStyled("dark", defaultStyles())
		tr.apply(agent.Event{Kind: agent.EvText, Text: text})
		tr.apply(agent.Event{Kind: agent.EvTurnDone})
		var lines []string
		for _, l := range strings.Split(plain(tr.render(renderOpts{width: 44})), "\n") {
			if strings.TrimSpace(l) != "" {
				lines = append(lines, strings.TrimRight(l, " "))
			}
		}
		return lines
	}
	col := func(l, s string) int {
		i := strings.Index(l, s)
		if i < 0 {
			return -1
		}
		return lipgloss.Width(l[:i])
	}

	got := render("- The file `hello.py` holds a single print statement that writes to stdout\n- short")
	if len(got) < 3 || col(got[1], "print") < 0 && col(got[1], "statement") < 0 {
		t.Fatalf("expected the first item to wrap:\n%s", strings.Join(got, "\n"))
	}
	text := col(got[0], "The")
	if lead := len(got[1]) - len(strings.TrimLeft(got[1], " ")); lead != text {
		t.Fatalf("a wrapped bullet starts at column %d, its text at %d:\n%s", lead, text, strings.Join(got, "\n"))
	}
	for _, l := range got {
		if w := lipgloss.Width(l); w > 44 {
			t.Fatalf("line wider than the window (%d): %q", w, l)
		}
	}

	got = render("12. a numbered item long enough that it has to wrap onto another line")
	if len(got) < 2 || len(got[1])-len(strings.TrimLeft(got[1], " ")) != col(got[0], "a numbered") {
		t.Fatalf("a wrapped numbered item does not hang under its text:\n%s", strings.Join(got, "\n"))
	}

	got = render("> a quoted remark that is long enough to wrap across several lines of the window\n>\n> and a second paragraph")
	bars := 0
	for _, l := range got {
		if !strings.HasPrefix(strings.TrimLeft(l, " "), "│") {
			t.Fatalf("a quote line lost its bar: %q\n%s", l, strings.Join(got, "\n"))
		}
		if strings.TrimSpace(l) == "│" {
			bars++
		}
	}
	if bars != 1 {
		t.Fatalf("the gap between quoted paragraphs: %d bar-only lines\n%s", bars, strings.Join(got, "\n"))
	}
}

// TestFlashClearsWithoutMovingTheLayout: the flash on a finished call is
// chrome. It has to go away on its own — a finished entry is rendered
// once and cached, so a flash that never cleared would stay bright for
// the rest of the session — and while it comes and goes nothing on the
// screen may move.
func TestFlashClearsWithoutMovingTheLayout(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)

	tr := newTranscript()
	c := agent.ToolCall{ID: "1", Name: "bash"}
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c, Output: "ok", Duration: 1200 * time.Millisecond})
	if got := time.Until(tr.flashUntil); got <= 0 || got > flashFor {
		t.Fatalf("a finished call must leave a deadline to flash until: %v", got)
	}

	bright := tr.render(renderOpts{width: 80})
	tr.findTool("1").ended = time.Now().Add(-time.Second) // the flash has faded
	settled := tr.render(renderOpts{width: 80})
	if bright == settled {
		t.Fatal("the flash never cleared: the cached render kept it bright")
	}
	if a, b := lipgloss.Height(bright), lipgloss.Height(settled); a != b {
		t.Fatalf("the flash moved the layout: %d lines bright, %d settled", a, b)
	}
	if a, b := plain(bright), plain(settled); a != b {
		t.Fatalf("the flash changed the text, not just the colour:\n%q\n%q", a, b)
	}
	if again := tr.render(renderOpts{width: 80}); again != settled {
		t.Fatal("a settled call must not go on being redrawn")
	}
}

// TestFlashIsInvisibleWithoutColour: on a terminal with only the sixteen
// ANSI colours the bright form is the ordinary form, so there is no
// flash and nothing to schedule.
func TestFlashIsInvisibleWithoutColour(t *testing.T) {
	sty := defaultStyles()
	sty.ansiOnly = true
	tr := newTranscriptStyled("dark", sty)
	c := agent.ToolCall{ID: "1", Name: "bash"}
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c, Output: "ok", Duration: time.Second})

	bright := tr.render(renderOpts{width: 80})
	tr.findTool("1").ended = time.Now().Add(-time.Second)
	if settled := tr.render(renderOpts{width: 80}); settled != bright {
		t.Fatalf("ansiOnly should not flash:\n%q\n%q", bright, settled)
	}
	m := &model{transcript: tr, styles: sty, pinned: true}
	if cmd := m.scheduleFlash(); cmd != nil {
		t.Fatal("ansiOnly scheduled a timer for a flash it does not draw")
	}
}

// TestFlashSchedulesOneTimer: twenty calls returning together must cost
// one timer, not twenty, and none at all when the user has scrolled away
// from the tail and cannot see the line.
func TestFlashSchedulesOneTimer(t *testing.T) {
	tr := newTranscript()
	m := &model{transcript: tr, styles: defaultStyles(), pinned: true}
	finish := func(id string) {
		c := agent.ToolCall{ID: id, Name: "bash"}
		tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c})
		tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c, Output: "ok"})
	}

	finish("1")
	if m.scheduleFlash() == nil {
		t.Fatal("the first finished call must arm the timer")
	}
	for i := 2; i <= 20; i++ {
		finish(fmt.Sprint(i))
		if cmd := m.scheduleFlash(); cmd != nil {
			t.Fatalf("call %d armed a second timer", i)
		}
	}
	// The tick that fires releases the latch; the deadline has moved on
	// with the later calls, so exactly one more timer covers them.
	m.flashPending = false
	if m.scheduleFlash() == nil {
		t.Fatal("the calls that finished meanwhile must be covered by one more timer")
	}
	m.flashPending = false
	tr.flashUntil = time.Now().Add(-time.Second)
	if m.scheduleFlash() != nil {
		t.Fatal("nothing is flashing: no timer")
	}

	tr.flashUntil = time.Now().Add(flashFor)
	m.pinned = false
	if m.scheduleFlash() != nil {
		t.Fatal("a reader who has scrolled away must not be made to pay for a redraw")
	}
}

// The one-line summary named the keys only. The prompt body two lines
// below it printed the whole argument JSON, so what the summary was
// careful about was on screen anyway — and the comment on the summary
// said the values were "one expand away", which they were not.
func TestPendingMCPArgumentsHideTheirValues(t *testing.T) {
	secret := "sk-secret-token-value"
	raw := `{"repo":"a/b","api_key":"` + secret + `","dry_run":true,"retries":3}`

	got := prettyInputFor("mcp__deploy__ship", raw, false)
	if strings.Contains(got, secret) {
		t.Fatalf("a value reached the prompt body: %q", got)
	}
	for _, want := range []string{"api_key", "repo", "dry_run", "true", "retries", "3", "ctrl+o"} {
		if !strings.Contains(got, want) {
			t.Errorf("the prompt body lost %q: %q", want, got)
		}
	}
	// Expanding is the deliberate act that shows them.
	if full := prettyInputFor("mcp__deploy__ship", raw, true); !strings.Contains(full, secret) {
		t.Errorf("expanding did not show the values: %q", full)
	}
	// A built-in tool is the harness's own code and is unchanged.
	if full := prettyInputFor("write_file", `{"path":"a","content":"`+secret+`"}`, false); !strings.Contains(full, secret) {
		t.Errorf("a built-in tool's input was hidden: %q", full)
	}
	// Arguments that are not an object leak nothing either.
	if got := prettyInputFor("mcp__x__y", `["`+secret+`"]`, false); strings.Contains(got, secret) {
		t.Errorf("a non-object argument leaked: %q", got)
	}
}

// A reply cancelled mid-stream can stop on the first characters of a
// heading or bullet; those are dropped rather than drawn as an empty
// heading under the "cancelled" line. A closing code fence is content.
func TestCancelledReplyDropsADanglingMarkdownStub(t *testing.T) {
	for in, want := range map[string]string{
		"Found it.\n\n###":          "Found it.",
		"Found it.\n- \n":           "Found it.",
		"###":                       "",
		"```go\nx := 1\n```":        "```go\nx := 1\n```",
		"Done. **bold** at the end": "Done. **bold** at the end",
	} {
		tr := newTranscript()
		tr.add(&entry{kind: entryAssistant, text: in})
		tr.dropEmptyTrailing()
		got := ""
		if len(tr.entries) > 0 {
			got = tr.entries[0].text
		}
		if got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

// Headings show their level in colour and weight, not in "#" marks the
// reader has to skip.
func TestMarkdownHeadingsDropTheirMarks(t *testing.T) {
	const src = "# Top\n\n## Summary\n\n### Files overview\n\n#### Deep\n\ntext\n"
	for _, style := range []string{"dark", "light"} {
		tr := newTranscriptStyled(style, defaultStyles())
		tr.apply(agent.Event{Kind: agent.EvText, Text: src})
		tr.apply(agent.Event{Kind: agent.EvTurnDone})
		out := plain(tr.render(renderOpts{width: 80, renderer: builtinRenderers}))
		if strings.Contains(out, "#") {
			t.Errorf("%s: heading marks left:\n%s", style, out)
		}
		for _, want := range []string{"Top", "Summary", "Files overview", "Deep"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: lost %q:\n%s", style, want, out)
			}
		}
	}
}

// A table is as wide as what it holds, inside a border, not stretched
// across the terminal with its last column running to the edge.
func TestMarkdownTablesFitTheirContent(t *testing.T) {
	const src = "Files:\n\n| File | Lines |\n|---|--:|\n| `hello.go` | 12 |\n| hello_test.go \\| x | 25 |\n\nafter\n"
	tr := newTranscriptStyled("dark", defaultStyles())
	tr.apply(agent.Event{Kind: agent.EvText, Text: src})
	tr.apply(agent.Event{Kind: agent.EvTurnDone})
	out := plain(tr.render(renderOpts{width: 100, renderer: builtinRenderers}))
	var rows []string
	for _, l := range strings.Split(out, "\n") {
		if strings.ContainsAny(l, "│╭╰├") {
			rows = append(rows, l)
		}
	}
	if len(rows) != 6 || !strings.HasPrefix(strings.TrimSpace(rows[0]), "╭") {
		t.Fatalf("table rows = %d:\n%s", len(rows), out)
	}
	for _, r := range rows {
		if w := lipgloss.Width(r); w > 40 {
			t.Fatalf("table row %d wide, stretched to the terminal:\n%s", w, out)
		}
	}
	if !strings.Contains(out, "hello_test.go | x") || strings.Contains(out, "`") {
		t.Fatalf("cells not rendered:\n%s", out)
	}
	// Right-aligned column: "12" sits against the right border.
	if !strings.Contains(out, " 12 │") {
		t.Fatalf("alignment lost:\n%s", out)
	}
	// A table wider than the terminal is wrapped to fit it.
	wide := "| a | b |\n|---|---|\n| " + strings.Repeat("word ", 40) + "| x |\n"
	tr = newTranscriptStyled("dark", defaultStyles())
	tr.apply(agent.Event{Kind: agent.EvText, Text: wide})
	tr.apply(agent.Event{Kind: agent.EvTurnDone})
	for _, l := range strings.Split(plain(tr.render(renderOpts{width: 60, renderer: builtinRenderers})), "\n") {
		if lipgloss.Width(l) > 60 {
			t.Fatalf("line %d wide at width 60: %q", lipgloss.Width(l), l)
		}
	}
}

func trimLineEnds(s string) string {
	ls := strings.Split(s, "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(ls, "\n")
}
