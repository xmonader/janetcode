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

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
)

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func plain(s string) string { return ansiRe.ReplaceAllString(s, "") }

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
	out := plain(tr.render(renderOpts{width: 80}))
	if !strings.Contains(out, "▶ read_file a.txt") {
		t.Fatalf("running tool not shown:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: full, Output: "1\talpha\n2\tbeta", Duration: 3 * time.Millisecond})
	tr.apply(agent.Event{Kind: agent.EvText, Text: "It says **alpha**"})
	tr.apply(agent.Event{Kind: agent.EvText, Text: " and beta."})
	tr.apply(agent.Event{Kind: agent.EvTextBlockDone})
	tr.apply(agent.Event{Kind: agent.EvTurnDone})
	out = plain(tr.render(renderOpts{width: 80}))
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
	if out := plain(tr.render(renderOpts{width: 80, pending: &permissionMsg{call: edit}})); !strings.Contains(out, "-old line\n") || !strings.Contains(out, "+new line") || strings.Contains(out, "+++ f") {
		t.Fatalf("diff preview missing:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &edit, Output: "Edited f"})
	if out := plain(tr.render(renderOpts{width: 80})); !strings.Contains(out, "+new line") {
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
	out = plain(tr.render(renderOpts{width: 80}))
	if !strings.Contains(out, "⊘ bash rm -rf build") {
		t.Fatalf("denied marker missing:\n%s", out)
	}
	tr.errorf("boom %d", 7)
	if out = plain(tr.render(renderOpts{width: 80})); !strings.Contains(out, "✗ boom 7") {
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
	if got := summarizeInput("grep", `{"pattern":"foo","path":"src"}`, 40, ""); got != "foo in src" {
		t.Fatalf("got %q", got)
	}
	if got := summarizeInput("bash", `{"command":"a\nb"}`, 40, ""); got != "a⏎ b" {
		t.Fatalf("got %q", got)
	}
	if got := summarizeInput("x", `{"k":"`+strings.Repeat("v", 50)+`"}`, 20, ""); len(got) > 22 || !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q", got)
	}
}

func TestThinkingView(t *testing.T) {
	if got := thinkingView("", false); got != "· thinking" {
		t.Fatalf("empty: %q", got)
	}
	long := strings.Repeat("line\n", 20) + "last"
	got := thinkingView(long, false)
	if !strings.HasPrefix(got, "· thinking… (19 earlier lines)") || !strings.HasSuffix(got, "last") || strings.Count(got, "\n") != 2 {
		t.Fatalf("streaming tail: %q", got)
	}
	if got := thinkingView("plan first\nthen do", true); got != "· thought: plan first (2 lines)" {
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
	if got := summarizeInput("read_file", `{"path":"/work/proj/internal/x/x.go","offset":120}`, 60, "/work/proj"); got != "internal/x/x.go from line 120" {
		t.Fatalf("got %q", got)
	}
	if got := summarizeInput("grep", `{"pattern":"foo","path":"/work/proj/src"}`, 60, "/work/proj"); got != "foo in src" {
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

// TestPlanUpdatesRenderAsDeltas: the first plan shows whole; a later
// rewrite shows only the lines that changed and the tally.
func TestPlanUpdatesRenderAsDeltas(t *testing.T) {
	first := "[ ] design\n[ ] parse\n[ ] eval\n\n0 of 3 done"
	second := "[>] design\n[ ] parse\n[ ] eval\n\n0 of 3 done"
	third := "[x] design\n[>] parse\n[ ] eval\n\n1 of 3 done"
	if planDelta("", first) != first {
		t.Fatal("first plan shows whole")
	}
	if got := planDelta(first, second); got != "[>] design\n0 of 3 done" {
		t.Fatalf("second = %q", got)
	}
	if got := planDelta(second, third); got != "[x] design\n[>] parse\n1 of 3 done" {
		t.Fatalf("third = %q", got)
	}
	if got := planDelta(third, third); got != third {
		t.Fatalf("unchanged plan shown whole: %q", got)
	}
	tr := newTranscript()
	c1 := agent.ToolCall{ID: "1", Name: "todo"}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &c1})
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c1})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c1, Output: first})
	c2 := agent.ToolCall{ID: "2", Name: "todo"}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &c2})
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &c2})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &c2, Output: second})
	out := plain(tr.renderTool(tr.findTool("2"), 80, renderOpts{}))
	if strings.Contains(out, "[ ] parse") || !strings.Contains(out, "[>] design") {
		t.Fatalf("second plan rendered whole:\n%s", out)
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
		tr := newTranscriptStyled(style)
		tr.apply(agent.Event{Kind: agent.EvText, Text: src})
		tr.apply(agent.Event{Kind: agent.EvTurnDone})
		out := tr.render(renderOpts{width: 80})
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
		c := markdownStyle(dark)
		accent, tool := colAccent.Light, colTool.Light
		if dark {
			accent, tool = colAccent.Dark, colTool.Dark
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
	live := thinkingView(text, false)
	if n := len(strings.Split(live, "\n")); n != 3 {
		t.Fatalf("streaming thought is %d lines, want 3:\n%s", n, live)
	}
	if !strings.Contains(live, "step 12") || !strings.Contains(live, "step 11") {
		t.Errorf("streaming thought lost the tail:\n%s", live)
	}
	if !strings.Contains(live, "10 earlier lines") {
		t.Errorf("streaming thought does not say what it hid:\n%s", live)
	}
	folded := thinkingView(text, true)
	if n := len(strings.Split(folded, "\n")); n != 1 {
		t.Fatalf("folded thought is %d lines, want 1:\n%s", n, folded)
	}
	if !strings.Contains(folded, "step 1") || !strings.Contains(folded, "12 lines") {
		t.Errorf("folded thought = %q", folded)
	}
	// Short thoughts are shown whole.
	if got := thinkingView("only one", false); got != "· only one" {
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
	out := plain(tr.render(renderOpts{width: 80}))
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
	out = plain(tr.render(renderOpts{width: 80}))
	// Command output keeps its tail: two lines of head, three of tail.
	if !strings.Contains(out, "… +7 lines") || !strings.Contains(out, "line 12") || strings.Contains(out, "line 6") {
		t.Errorf("twelve lines were not folded head 2 / tail 3:\n%s", out)
	}

	// read_file folds to one line, and expands again with ctrl+o.
	tr = newTranscript()
	tr.apply(agent.Event{Kind: agent.EvToolRun, Call: &read})
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &read, Output: body + "... (36 more lines)\n"})
	out = plain(tr.render(renderOpts{width: 80}))
	if !strings.Contains(out, "read main.go: 6 of 42 lines") {
		t.Errorf("read_file not folded:\n%s", out)
	}
	if strings.Contains(out, "line 3") {
		t.Errorf("read_file still prints the file:\n%s", out)
	}
	if out = plain(tr.render(renderOpts{width: 80, verbose: true})); !strings.Contains(out, "line 3") {
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
	out = plain(tr.render(renderOpts{width: 80}))
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
	out := tr.render(renderOpts{width: 80})
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
	if tipOnly.render(renderOpts{width: 80}) == infoOnly.render(renderOpts{width: 80}) {
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
	out := plain(renderPreview(preview, 40, 10))
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
	o := renderOpts{width: 80}
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
	tr.render(renderOpts{width: 80})
	last := tr.entries[len(tr.entries)-1]
	if got := tr.entryAtLine(last.lineFrom); got != last {
		t.Fatalf("entryAtLine(%d) found the wrong entry", last.lineFrom)
	}
	if tr.entryAtLine(tr.entries[0].lineFrom) != nil {
		t.Fatal("a user line is not expandable")
	}
}

// TestFoldedThoughtReopens: a thought folded to one line can be read again.
func TestFoldedThoughtReopens(t *testing.T) {
	tr := newTranscript()
	tr.apply(agent.Event{Kind: agent.EvThinking, Text: "one\ntwo\nthree\nfour"})
	tr.apply(agent.Event{Kind: agent.EvTextBlockDone})
	o := renderOpts{width: 80}
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
	out := plain(tr.render(renderOpts{width: 100}))
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
