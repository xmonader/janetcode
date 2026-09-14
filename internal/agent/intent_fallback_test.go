package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// This file exercises intentFor's fallback order one level at a time (see
// provenance.go): a call's own stated intent, text before the call in the
// same message, a thinking block in that message, and the previous
// assistant message's text within the same turn. Models like qwen put
// their reasoning somewhere other than "text right before the call", so
// each of these has to work on its own.

// TestIntentFallbackExplicitWins covers level 1: when the call itself
// carries an intent (as tool.TakeIntent extracts when the intent feature
// is on), it is used even though the same message also has text that
// would otherwise be picked up by level 2.
func TestIntentFallbackExplicitWins(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "text before the call"},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: json.RawMessage(`{"intent":"explicit stated intent","path":"foo.txt","content":"hi\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.Tools.Intent = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 1 || changes[0].Intent != "explicit stated intent" {
		t.Fatalf("changes = %+v", changes)
	}
}

// TestIntentFallbackTextBeforeCall covers level 2: no explicit intent, so
// the text written just before the tool_use block in the same message is
// used, as it always was.
func TestIntentFallbackTextBeforeCall(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Create the config file.\nMore detail."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"foo.txt","content":"hi\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 1 || changes[0].Intent != "Create the config file." {
		t.Fatalf("changes = %+v", changes)
	}
}

// TestIntentFallbackThinkingBlock covers level 3: a model (qwen-shaped)
// that puts its reasoning in a thinking block and writes no text at all
// before the call still gets a one-line intent, from the thinking block's
// first line.
func TestIntentFallbackThinkingBlock(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: "I should create the config file now.\nBecause the user asked for it."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"foo.txt","content":"hi\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 1 || changes[0].Intent != "I should create the config file now." {
		t.Fatalf("changes = %+v", changes)
	}
}

// TestIntentFallbackPreviousAssistantMessage covers level 4: a model that
// explains itself on the first step of a turn and then just calls tools on
// later steps within that same turn. The second step's message has neither
// text nor a thinking block, so the intent for its call falls back to the
// last text of the previous assistant message in the same turn.
func TestIntentFallbackPreviousAssistantMessage(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Plan: read a.txt, then write foo.txt."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "c2", Name: "write_file", Input: json.RawMessage(`{"path":"foo.txt","content":"hi\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 1 || changes[0].Path == "" {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].Intent != "Plan: read a.txt, then write foo.txt." {
		t.Fatalf("intent = %q, want the previous assistant message's text", changes[0].Intent)
	}
}

// TestIntentFallbackNoTurnBleed makes sure level 4 never reaches back into
// an earlier turn: a first turn ends with an assistant text and no tool
// call, and a second turn's tool call (with nothing said before it) must
// not pick up the first turn's text as its intent.
func TestIntentFallbackNoTurnBleed(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		text("Turn one: nothing to do with turn two."),
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"foo.txt","content":"hi\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "second", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 1 {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[0].Intent != "" {
		t.Fatalf("intent = %q, want empty: it must not borrow the previous turn's text", changes[0].Intent)
	}
}

// TestIntentSkipsBoilerplateOpeners: a thinking block that opens by
// restating the request ("The user wants me to:") labelled every change
// with that line. The first line that says something is the intent.
func TestIntentSkipsBoilerplateOpeners(t *testing.T) {
	for in, want := range map[string]string{
		"The user wants me to:\n1. Clip long lines in the status bar\n2. Add a test":             "Clip long lines in the status bar",
		"Let me look at this.\nI'll start by reading clip.go.\nFix the off-by-one in ClipBytes.": "Fix the off-by-one in ClipBytes.",
		"I need to understand the request:\n- rename compute to calculate":                       "rename compute to calculate",
		"Create the config file.\nMore detail.":                                                  "Create the config file.",
		// Nothing but openers: the first line is still better than nothing.
		"Let me fix the parser.": "Let me fix the parser.",
		"Now I understand:":      "Now I understand:",
	} {
		if got := intentLine(in, 120); got != want {
			t.Errorf("intentLine(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestIntentFallbackDoesNotBorrowAnotherChangesLabel: level 4 borrows an
// earlier message's explanation only when that message changed nothing
// itself. When its own call made a change, the text was that change's
// label, and reusing it put one change's reason on another in /changes.
func TestIntentFallbackDoesNotBorrowAnotherChangesLabel(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Rename compute to calculate in a.txt."},
			{Type: llm.BlockToolUse, ID: "c1", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"calculate\n"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "c2", Name: "write_file", Input: json.RawMessage(`{"path":"foo.txt","content":"hi\n"}`)},
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	changes := a.Changes()
	if len(changes) != 2 || changes[0].Intent != "Rename compute to calculate in a.txt." {
		t.Fatalf("changes = %+v", changes)
	}
	if changes[1].Intent != "" {
		t.Fatalf("intent of the second change = %q, want empty: that text was the first change's label", changes[1].Intent)
	}
}
