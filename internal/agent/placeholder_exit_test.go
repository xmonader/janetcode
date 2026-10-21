package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// The bash tool appends its own "[exit code N]" after the output, so the
// last one is the command's. A command whose output carries the same
// shape — grepping a saved transcript, a test that prints one — was
// remembered by its first, and a green run went into the compacted
// history and the working memory as a failure.
func TestExitCodeIsTheLastOneInTheOutput(t *testing.T) {
	out := "old log:\n[exit code 2]\nall good\n[exit code 0]"
	in := json.RawMessage(`{"command":"grep exit run.log; true"}`)
	if ph := placeholder("bash", in, out, ""); !strings.Contains(ph, "→ exit 0") {
		t.Errorf("placeholder = %q, want exit 0", ph)
	}
	var w WorkingMemory
	w.absorb([]llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "c1", Name: "bash", Input: in}}},
		llm.ToolResultMessage(llm.ToolResult("c1", out, false)),
	})
	if len(w.Commands) != 1 || w.Commands[0].Exit != "0" {
		t.Errorf("working memory commands = %+v, want exit 0", w.Commands)
	}
}
