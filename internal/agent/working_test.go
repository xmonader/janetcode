package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestWorkingMemoryReplacesHandoffNote(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	use := func(id, name, input string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}}
	}
	res := func(id, text string, isErr bool) llm.Message {
		return llm.ToolResultMessage(llm.ToolResult(id, text, isErr))
	}
	long := strings.Repeat("x", 3000)
	f := &fake{responses: []*llm.Response{text(`Here you go:
{"goal":"add the feature; no new dependencies","decisions":["keep it in package a"],"ruled_out":["a generic helper: over-engineering"],"facts":["a.go holds the entry point"],"open":[],"next":["run the race suite","commit"]}`)}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Hooks: &hook.Runner{Cwd: root}, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "add the feature without new dependencies"),
		use("1", "read_file", `{"path":"a.go"}`), res("1", long, false),
		use("2", "edit_file", `{"path":"a.go","old_string":"x","new_string":"y"}`), res("2", "edited", false),
		use("3", "edit_file", `{"path":"a.go","old_string":"y","new_string":"z"}`), res("3", "edited", false),
		use("4", "write_file", `{"path":"gone.go","content":"x"}`), res("4", "Error: denied", true),
		use("5", "bash", `{"command":"go test ./..."}`), res("5", long+"\nFAIL\n[exit code 1]", false),
		use("6", "bash", `{"command":"go test ./..."}`), res("6", "ok\n[exit code 0]", false),
		llm.TextMessage(llm.RoleAssistant, "done"),
	}}
	// The recent tail is kept verbatim, so pad it with chat and keep the
	// tool calls in the summarised head.
	a.Messages = append(a.Messages, chatter(3)...)
	a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, "now the tests"))
	a.integ.userText = "now the tests"
	a.working.Verify = "verify passed: go test ./... (1.2s)"
	if err := a.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	ask := f.requests[0].Messages[len(f.requests[0].Messages)-1].Text()
	if !strings.Contains(ask, `"goal": "add the feature without new dependencies"`) {
		t.Fatalf("previous record not sent: %q", ask)
	}
	note := a.Messages[0].Text()
	sum := a.working.Edited[0].Hash
	for _, want := range []string{
		workingMarker,
		"Goal: add the feature; no new dependencies",
		"Current request: now the tests",
		"- a.go  2 edits  " + sum,
		"Files read: a.go",
		`- "go test ./..." → exit 0 (2 runs)`,
		"Last verification: verify passed",
		"Decisions:\n- keep it in package a",
		"Ruled out:\n- a generic helper",
		"Facts about the codebase:\n- a.go holds",
		"Next:\n1. run the race suite\n2. commit",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "gone.go") || strings.Contains(note, "Open questions") || len(sum) != 8 {
		t.Fatalf("failed edit counted, empty section rendered, or bad hash %q:\n%s", sum, note)
	}
	if !strings.Contains(note, "Files edited") || strings.Index(note, "Files edited") > strings.Index(note, "Decisions") {
		t.Fatal("harness-observed sections come before the model's")
	}

	// A second compaction updates the record instead of starting over:
	// counts carry, the tail is not counted twice, and prose from the
	// model lands in Notes instead of being lost.
	f.responses = append(f.responses, text("I could not produce JSON; the work is nearly done."))
	a.Messages = append(a.Messages,
		use("7", "edit_file", `{"path":"a.go","old_string":"z","new_string":"w"}`), res("7", "edited", false),
		llm.TextMessage(llm.RoleAssistant, "ok"),
	)
	a.Messages = append(a.Messages, chatter(3)...)
	a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, "more"))
	var evs []Event
	if err := a.ForceCompact(context.Background(), func(e Event) {
		if e.Kind != EvPhase {
			evs = append(evs, e)
		}
	}); err != nil {
		t.Fatal(err)
	}
	note = a.Messages[0].Text()
	if !strings.Contains(note, "- a.go  3 edits") || !strings.Contains(note, "Notes:\nI could not produce JSON") || !strings.Contains(note, "Decisions:\n- keep it in package a") {
		t.Fatalf("second note:\n%s", note)
	}
	if len(evs) != 2 || !strings.Contains(evs[0].Text, "prose") {
		t.Fatalf("events = %+v", evs)
	}

	// The switch restores the prose handoff note.
	b := &Agent{Provider: &fake{responses: []*llm.Response{text("PROSE")}}, Tools: tool.NewRegistry(), Model: "m", Off: map[string]bool{"working": true}, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "first"), llm.TextMessage(llm.RoleAssistant, "ok"),
	}}
	if err := b.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := b.Messages[0].Text(); !strings.HasPrefix(got, "[Context was compacted. Handoff note") || !strings.Contains(got, "PROSE") {
		t.Fatalf("prose note = %q", got)
	}
}

// chatter is n user/assistant exchanges of plain text.
func chatter(n int) []llm.Message {
	var out []llm.Message
	for i := 0; i < n; i++ {
		out = append(out, llm.TextMessage(llm.RoleUser, "u"), llm.TextMessage(llm.RoleAssistant, "a"))
	}
	return out
}

func TestWorkingMemoryBounds(t *testing.T) {
	var w WorkingMemory
	var msgs []llm.Message
	for i := 0; i < maxCommands+5; i++ {
		id := string(rune('a' + i))
		msgs = append(msgs,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: "bash", Input: json.RawMessage(`{"command":"cmd ` + id + `"}`)}}},
			llm.ToolResultMessage(llm.ToolResult(id, "[exit code 2]", false)))
	}
	w.absorb(msgs)
	if len(w.Commands) != maxCommands || w.Commands[0].Command != "cmd f" || w.Commands[maxCommands-1].Exit != "2" {
		t.Fatalf("commands = %+v", w.Commands)
	}
	if w.merge("{\"next\":[\" a \",\"\",\"b\"]}") != true || len(w.Next) != 2 || w.Next[0] != "a" {
		t.Fatalf("next = %v", w.Next)
	}
	if w.merge("no json here") || w.Notes != "no json here" {
		t.Fatal("prose should land in Notes")
	}
}

// A server that numbers calls per response sends call_0 every time; the
// harness renames the second (callids.go) but its result still carries
// call_0. Each result must be counted against the call it answers, not
// against the first call that ever had that id — which would drop the
// second command as already seen.
func TestWorkingMemoryCountsCallsThatShareAServerID(t *testing.T) {
	use := func(id, reply, cmd string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ID: id, ReplyID: reply, Name: "bash",
			Input: json.RawMessage(fmt.Sprintf(`{"command":%q}`, cmd)),
		}}}
	}
	result := func(text string) llm.Message {
		return llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_0", Text: text}}}
	}
	var w WorkingMemory
	w.absorb([]llm.Message{
		llm.TextMessage(llm.RoleUser, "build it"),
		use("call_0", "", "go test ./..."), result("exit 1"),
		use("call_0#2", "call_0", "go build ./..."), result("ok"),
	})
	var got []string
	for _, c := range w.Commands {
		got = append(got, c.Command)
	}
	if len(got) != 2 || got[0] != "go test ./..." || got[1] != "go build ./..." {
		t.Fatalf("commands = %q, want both, each once", got)
	}
	// A later compaction's head with another call_0 is not taken for one
	// already counted.
	w.absorb([]llm.Message{use("call_0#3", "call_0", "go vet ./..."), result("ok")})
	if len(w.Commands) != 3 {
		t.Fatalf("commands after a later absorb = %+v", w.Commands)
	}
}

func TestAsksSurviveCompactionVerbatim(t *testing.T) {
	var w WorkingMemory
	w.absorb([]llm.Message{
		llm.TextMessage(llm.RoleUser, "Add rate limiting to the API server."),
		llm.TextMessage(llm.RoleAssistant, "ok"),
		llm.TextMessage(llm.RoleUser, "Also make the limit configurable via LIMIT_RPS."),
		llm.TextMessage(llm.RoleUser, "[harness note: not a request]"),
	})
	if len(w.Asks) != 2 || w.Goal != "Add rate limiting to the API server." {
		t.Fatalf("asks = %v goal = %q", w.Asks, w.Goal)
	}
	out := w.Render()
	if !strings.Contains(out, "User requests so far") || !strings.Contains(out, "2. Also make the limit configurable via LIMIT_RPS.") {
		t.Fatalf("render:\n%s", out)
	}
	// Bounded: the first ask and the latest survive, the middle is dropped.
	for i := 0; i < 20; i++ {
		w.absorb([]llm.Message{llm.TextMessage(llm.RoleUser, fmt.Sprintf("request %d", i))})
	}
	if len(w.Asks) != maxAsks || w.Asks[0] != w.Goal || w.Asks[maxAsks-1] != "request 19" {
		t.Fatalf("asks after 20 more = %v", w.Asks)
	}
}

// The note must say it is a summary and point at the archive: a model
// that takes it as the whole truth reconstructs exact values it no longer
// has, plausibly and wrongly.
func TestRenderClosesWithFaultDiscipline(t *testing.T) {
	var w WorkingMemory
	w.Goal = "fix the build"
	out := w.Render()
	if !strings.HasSuffix(out, faultDiscipline) || !strings.Contains(out, "expand_output") {
		t.Fatalf("render:\n%s", out)
	}
}
