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

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

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
	out := plain(tr.render(80, false, nil))
	if !strings.Contains(out, "▶ read_file a.txt") {
		t.Fatalf("running tool not shown:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: full, Output: "1\talpha\n2\tbeta", Duration: 3 * time.Millisecond})
	tr.apply(agent.Event{Kind: agent.EvText, Text: "It says **alpha**"})
	tr.apply(agent.Event{Kind: agent.EvText, Text: " and beta."})
	tr.apply(agent.Event{Kind: agent.EvTextBlockDone})
	tr.apply(agent.Event{Kind: agent.EvTurnDone})
	out = plain(tr.render(80, false, nil))
	for _, want := range []string{"you", "read a.txt please", "· thought: let me look", "✓ read_file a.txt", "alpha", "beta", "and beta."} {
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
	if out := plain(tr.render(80, false, &permissionMsg{call: edit})); !strings.Contains(out, "-old line\n") || !strings.Contains(out, "+new line") || strings.Contains(out, "+++ f") {
		t.Fatalf("diff preview missing:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &edit, Output: "Edited f"})
	if out := plain(tr.render(80, false, nil)); !strings.Contains(out, "+new line") {
		t.Fatalf("diff should stay visible after completion:\n%s", out)
	}
	tr.apply(agent.Event{Kind: agent.EvToolStart, Call: &call})
	pending := &permissionMsg{call: call}
	out := plain(tr.render(80, false, pending))
	if !strings.Contains(out, "allow bash? [y]es") || !strings.Contains(out, `"command": "rm -rf build"`) {
		t.Fatalf("permission prompt missing:\n%s", out)
	}
	tr.permissionAnswered(call, agent.Deny)
	tr.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: "denied", IsError: true})
	out = plain(tr.render(80, false, nil))
	if !strings.Contains(out, "⊘ bash rm -rf build") {
		t.Fatalf("denied marker missing:\n%s", out)
	}
	tr.errorf("boom %d", 7)
	if out = plain(tr.render(80, false, nil)); !strings.Contains(out, "✗ boom 7") {
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
	out := plain(tr.render(60, false, nil))
	for _, want := range []string{"hi", "✓ glob *.go", "main.go", "one file"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestSummarizeInput(t *testing.T) {
	if got := summarizeInput("grep", `{"pattern":"foo","path":"src"}`, 40); got != "foo in src" {
		t.Fatalf("got %q", got)
	}
	if got := summarizeInput("bash", `{"command":"a\nb"}`, 40); got != "a⏎ b" {
		t.Fatalf("got %q", got)
	}
	if got := summarizeInput("x", `{"k":"`+strings.Repeat("v", 50)+`"}`, 20); len(got) > 22 || !strings.HasSuffix(got, "…") {
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
