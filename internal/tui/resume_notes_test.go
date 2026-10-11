package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// Notes the harness wrote into the user role — an undo, a redo, the
// repository map — are not prompts the person typed, and a resumed
// session must not replay them as "❯ …" rows.
func TestLoadHistoryHarnessNotesAreNotPrompts(t *testing.T) {
	tr := newTranscript()
	tr.loadHistory([]llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "[repository map: 3 files]\na.go\nb.go", Synthetic: true},
			{Type: llm.BlockText, Text: "fix the bug"},
		}},
		llm.TextMessage(llm.RoleAssistant, "done"),
		llm.HarnessMessage(llm.RoleUser, "The user undid the previous turn: its file changes were reverted and its 2 message(s) removed from this conversation."),
		// Written before redo notes were marked synthetic.
		llm.TextMessage(llm.RoleUser, "I reapplied the file changes I had undone:\na.go"),
		llm.TextMessage(llm.RoleUser, "[WIP] keep going"),
	})
	out := plain(tr.render(renderOpts{width: 200}))
	for _, bad := range []string{"❯ The user undid", "❯ I reapplied", "❯ [repository map", "b.go"} {
		if strings.Contains(out, bad) {
			t.Errorf("harness text %q replayed as a prompt:\n%s", bad, out)
		}
	}
	for _, want := range []string{"❯ fix the bug", "❯ [WIP] keep going", "The user undid the previous turn", "I reapplied the file changes"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
