package tui

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

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
	if !strings.HasPrefix(got, "· thinking… (13 earlier lines)") || !strings.HasSuffix(got, "last") || strings.Count(got, "\n") != 8 {
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
	if e := tr.last(entryInfo); e == nil || !strings.HasPrefix(e.text, "📎 ") {
		t.Fatalf("attach note = %+v", e)
	}
	if verdictBrief("✓ verified: tests that had been failing now pass") != "✓ verified" || verdictBrief("") != "" {
		t.Fatal("verdictBrief")
	}
}
