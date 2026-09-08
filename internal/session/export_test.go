package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestMarkdownExport(t *testing.T) {
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "fix the bug"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: "hmm"},
			{Type: llm.BlockText, Text: "Looking."},
			{Type: llm.BlockToolUse, ID: "1", Name: "read_file", Input: json.RawMessage(`{"path":"a.go"}`)},
		}},
		llm.ToolResultMessage(llm.ToolResult("1", strings.Repeat("x", 3000)+"```", true)),
		llm.TextMessage(llm.RoleAssistant, "Done."),
	}
	md := Markdown(Meta{ID: "s1", Cwd: "/w", Provider: "p", Model: "m", Created: time.Unix(0, 0)}, msgs)
	for _, want := range []string{"# janetcode session s1", "## User\n\nfix the bug", "<details><summary>Thinking</summary>", "**Tool call** `read_file`", `{"path":"a.go"}`, "**Error**", "… [1003 more bytes]", "## Assistant\n\nDone."} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in:\n%s", want, md)
		}
	}
	if strings.Count(md, "```") != 4 {
		t.Fatalf("fences unbalanced:\n%s", md)
	}
}
