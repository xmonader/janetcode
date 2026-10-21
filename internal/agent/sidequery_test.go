package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/llm"
)

// A side query (the advisor, the goal roles) builds its own request out
// of the tail of the conversation. Anthropic refuses a request whose
// first message is from the assistant, and the naive "last N messages"
// window lands on one whenever N falls that way.
func TestSideTranscriptStartsOnAUserMessage(t *testing.T) {
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "first"),
		llm.TextMessage(llm.RoleAssistant, "second"),
		llm.TextMessage(llm.RoleUser, "third"),
		llm.TextMessage(llm.RoleAssistant, "fourth"),
	}
	for keep := 1; keep <= len(msgs)+2; keep++ {
		got := sideTranscript(msgs, keep, 0, 0)
		if len(got) == 0 {
			continue
		}
		if got[0].Role != llm.RoleUser {
			t.Fatalf("keep=%d: transcript starts with %s", keep, got[0].Role)
		}
	}
}

// A tool result that printed only whitespace flattens to nothing. Sent as
// an empty text block it is refused (Anthropic: text content blocks must
// be non-empty), so the side query fails on an ordinary transcript.
func TestSideTranscriptDropsAnEmptyToolResult(t *testing.T) {
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "check the build"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "1", Name: "bash"}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "1", Text: "\n  \n"}}},
	}
	for _, m := range sideTranscript(msgs, 8, 0, 0) {
		for _, b := range m.Blocks {
			if strings.TrimSpace(b.Text) == "" {
				t.Fatalf("an empty text block went into the side transcript: %+v", m)
			}
		}
	}
}

func TestSideTranscriptCutsOnRuneBoundaries(t *testing.T) {
	long := strings.Repeat("é", 500)
	msgs := []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: long}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "1", Name: "read_file"}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "1", Text: long}}},
	}
	for n := 1; n < 60; n++ {
		for _, m := range sideTranscript(msgs, 8, n, n) {
			for _, b := range m.Blocks {
				if !utf8.ValidString(b.Text) {
					t.Fatalf("cap=%d produced invalid UTF-8: %q", n, b.Text)
				}
			}
		}
	}
}
