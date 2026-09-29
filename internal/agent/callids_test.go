package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func toolUse(id, name string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(`{}`)}
}

func TestNormalizeCallIDsSuffixesCollisionsAndKeepsTheOriginal(t *testing.T) {
	used := map[string]bool{}
	first := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{toolUse("call_0", "read_file")}}
	normalizeCallIDs(&first, used, 1, 1)
	if first.Blocks[0].ID != "call_0" || first.Blocks[0].ReplyID != "" {
		t.Fatalf("an id of its own was renamed: %+v", first.Blocks[0])
	}
	// A server that numbers calls per response reuses call_0 next turn.
	second := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{toolUse("call_0", "grep"), toolUse("call_0", "glob")}}
	normalizeCallIDs(&second, used, 2, 1)
	a, b := second.Blocks[0], second.Blocks[1]
	if a.ID == "call_0" || b.ID == "call_0" || a.ID == b.ID {
		t.Fatalf("ids collided: %q %q", a.ID, b.ID)
	}
	// The reply still goes back under the id the server knows.
	if replyID(a) != "call_0" || replyID(b) != "call_0" {
		t.Fatalf("reply ids = %q %q", replyID(a), replyID(b))
	}
	if a.WireID() != "call_0" {
		t.Fatalf("the wire id changed: %q", a.WireID())
	}
}

func TestNormalizeCallIDsSynthesisesMissingOnes(t *testing.T) {
	used := map[string]bool{}
	m := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{toolUse("", "read_file"), toolUse("", "grep")}}
	normalizeCallIDs(&m, used, 3, 2)
	a, b := m.Blocks[0], m.Blocks[1]
	if a.ID == "" || b.ID == "" || a.ID == b.ID {
		t.Fatalf("ids = %q %q", a.ID, b.ID)
	}
	if !strings.Contains(a.ID, "3_2") {
		t.Fatalf("a synthesised id should say where it came from: %q", a.ID)
	}
	// Nothing to reply under, so the reply uses the harness's own id.
	if replyID(a) != a.ID {
		t.Fatalf("reply id = %q", replyID(a))
	}
}

// The whole point: a result must reach the call the harness keyed by,
// while the provider still sees the id it sent.
func TestRepeatedProviderIDsStillPairUp(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			toolUse("call_0", "read_file"),
		}}, StopReason: llm.StopToolUse},
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			toolUse("call_0", "read_file"),
		}}, StopReason: llm.StopToolUse},
		text("done"),
	}}
	for _, r := range f.responses[:2] {
		r.Message.Blocks[0].Input = json.RawMessage(`{"path":"a.txt"}`)
	}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "read it twice", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int{}
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolUse {
				ids[b.ID]++
			}
		}
	}
	for id, n := range ids {
		if n > 1 {
			t.Fatalf("id %q names %d calls", id, n)
		}
	}
}

// A resumed conversation already holds ids the server handed out; a
// server that numbers calls per response sends call_0 again on the first
// turn after the resume, and it must not be taken for the old call. Both
// ways a history arrives are covered: assigned before the first turn (a
// process started with --resume) and swapped in by OpenSession on an
// agent that has already run (/resume inside a session).
func TestResumedHistoryIDsAreNotReused(t *testing.T) {
	history := func() []llm.Message {
		return []llm.Message{
			llm.TextMessage(llm.RoleUser, "read it"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{toolUse("call_0", "read_file")}},
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_0", Text: "alpha"}}},
			llm.TextMessage(llm.RoleAssistant, "read"),
		}
	}
	assertUnique := func(t *testing.T, msgs []llm.Message) {
		t.Helper()
		ids := map[string]int{}
		for _, m := range msgs {
			for _, b := range m.Blocks {
				if b.Type == llm.BlockToolUse {
					ids[b.ID]++
				}
			}
		}
		for id, n := range ids {
			if n > 1 {
				t.Fatalf("id %q names %d calls", id, n)
			}
		}
	}
	turn := func() *fake {
		return &fake{responses: []*llm.Response{toolCall("call_0", "read_file", `{"path":"a.txt"}`), text("done")}}
	}

	t.Run("assigned at startup", func(t *testing.T) {
		a, _ := newAgent(t, turn(), nil)
		a.Messages = history()
		if err := a.Run(context.Background(), "again", func(Event) {}); err != nil {
			t.Fatal(err)
		}
		assertUnique(t, a.Messages)
	})
	t.Run("opened after a turn", func(t *testing.T) {
		a, _ := newAgent(t, &fake{responses: []*llm.Response{text("hi")}}, nil)
		if err := a.Run(context.Background(), "first", func(Event) {}); err != nil {
			t.Fatal(err)
		}
		a.setMessages(history())
		a.Provider = turn()
		if err := a.Run(context.Background(), "again", func(Event) {}); err != nil {
			t.Fatal(err)
		}
		assertUnique(t, a.Messages)
	})
}
