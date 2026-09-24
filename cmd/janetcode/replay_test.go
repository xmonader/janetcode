package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// A steering message is a second user message inside a running turn. The
// replay must still drive exactly one Run per recorded turn, each with that
// turn's own prompt: driving one Run per user message (what --replay used
// to do) gave recorded turn 2 the steering text and never ran turn 2's own
// prompt.
func TestReplayDrivesRecordedTurnsDespiteSteering(t *testing.T) {
	sess, err := session.New(t.TempDir(), t.TempDir(), "anthropic", "m")
	if err != nil {
		t.Fatal(err)
	}
	add := func(e session.Event) {
		t.Helper()
		if _, err := sess.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	user := func(turn int, text string) {
		m := llm.TextMessage(llm.RoleUser, text)
		add(session.Event{Type: session.TypeUserMessage, Turn: turn, Surface: true, Message: &m})
	}
	callTool := func(turn int, id, name, args string) {
		m := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(args)}}}
		add(session.Event{Type: session.TypeAssistant, Turn: turn, Surface: true, Message: &m})
		add(session.Event{Type: session.TypeToolCall, Turn: turn, CallID: id, Name: name, Arguments: args})
		r := llm.ToolResultMessage(llm.ToolResult(id, "ok", false))
		add(session.Event{Type: session.TypeToolResult, Turn: turn, CallID: id, Name: name, Output: "ok", Surface: true, Message: &r})
	}
	finish := func(turn int) {
		m := llm.TextMessage(llm.RoleAssistant, "done")
		add(session.Event{Type: session.TypeAssistant, Turn: turn, Surface: true, Message: &m})
		add(session.Event{Type: session.TypeTurnEnd, Turn: turn, Reason: "end_turn"})
	}

	add(session.Event{Type: session.TypeTurnStart, Turn: 1})
	user(1, "list the files")
	callTool(1, "c1", "list_dir", `{"path":"."}`)
	user(1, "also look for go files") // steering, mid-turn
	callTool(1, "c2", "glob", `{"pattern":"*.go"}`)
	// A call a recipe made on the model's behalf: not a model decision.
	add(session.Event{Type: session.TypeToolCall, Turn: 1, CallID: "recipe-x-1", Name: "read_file", Arguments: `{"path":"x"}`})
	finish(1)
	add(session.Event{Type: session.TypeTurnStart, Turn: 2})
	user(2, "read main.go")
	callTool(2, "c3", "read_file", `{"path":"main.go"}`)
	finish(2)

	recorded, now, err := replaySession(context.Background(), sess.Path(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	texts := func(events []session.Event) []string {
		var out []string
		for _, tp := range session.Turns(events) {
			out = append(out, tp.Text)
		}
		return out
	}
	if want, got := []string{"list the files", "read main.go"}, texts(now); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed turn prompts = %q, want %q", got, want)
	}
	if want := []string{"list_dir", "glob", "read_file"}; !reflect.DeepEqual(modelCalls(recorded), want) {
		t.Fatalf("recorded calls = %v, want %v (the recipe's call left out)", modelCalls(recorded), want)
	}
	if !reflect.DeepEqual(modelCalls(now), modelCalls(recorded)) {
		t.Fatalf("now = %v, recorded = %v", modelCalls(now), modelCalls(recorded))
	}
}

// A recorded session that edited a file or ran a command replays to the
// same call sequence. The replay agent only had the four read tools, so
// every write_file came back as "now: (nothing)" and was reported as a
// regression. The mutating calls are answered by stubs: nothing runs.
func TestReplayOfAnEditingSessionMatches(t *testing.T) {
	sess, err := session.New(t.TempDir(), t.TempDir(), "openai", "m")
	if err != nil {
		t.Fatal(err)
	}
	add := func(e session.Event) {
		t.Helper()
		if _, err := sess.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(t.TempDir(), "ran")
	add(session.Event{Type: session.TypeTurnStart, Turn: 1})
	u := llm.TextMessage(llm.RoleUser, "create hello.txt")
	add(session.Event{Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &u})
	for i, c := range []struct{ name, args string }{
		{"write_file", `{"path":"hello.txt","content":"hi"}`},
		{"bash", `{"command":"touch ` + marker + `"}`},
	} {
		id := fmt.Sprintf("c%d", i)
		m := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: c.name, Input: json.RawMessage(c.args)}}}
		add(session.Event{Type: session.TypeAssistant, Turn: 1, Surface: true, Message: &m})
		add(session.Event{Type: session.TypeToolCall, Turn: 1, CallID: id, Name: c.name, Arguments: c.args})
		r := llm.ToolResultMessage(llm.ToolResult(id, "ok", false))
		add(session.Event{Type: session.TypeToolResult, Turn: 1, CallID: id, Name: c.name, Output: "ok", Surface: true, Message: &r})
	}
	done := llm.TextMessage(llm.RoleAssistant, "done")
	add(session.Event{Type: session.TypeAssistant, Turn: 1, Surface: true, Message: &done})
	add(session.Event{Type: session.TypeTurnEnd, Turn: 1, Reason: "end_turn"})

	recorded, now, err := replaySession(context.Background(), sess.Path(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareCalls(modelCalls(recorded), modelCalls(now), io.Discard); err != nil {
		t.Fatalf("now = %v, recorded = %v: %v", modelCalls(now), modelCalls(recorded), err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the replay ran the recorded bash command")
	}
	// A different sequence is a failure the exit status carries.
	if compareCalls([]string{"write_file"}, nil, io.Discard) == nil {
		t.Fatal("a changed call sequence returned no error")
	}
}
