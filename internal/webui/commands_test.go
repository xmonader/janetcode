package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestDispatchCoversWebCommands checks webCommands, the slash table and
// webDispatch against each other. webCommands is what the palette, /help
// and the commands API are all built from, so a command that only has a
// handler is invisible everywhere else, and one that is listed but has no
// handler answers "unknown command". It mirrors internal/tui's
// TestDispatchCoversTheTable for the terminal dispatcher.
func TestDispatchCoversWebCommands(t *testing.T) {
	known := map[string]bool{}
	for _, name := range webCommands {
		known[name] = true
		if _, ok := slash.Lookup(name); !ok {
			t.Errorf("%s is in webCommands but not in the slash table", name)
		}
		if webDispatch[name] == nil {
			t.Errorf("%s is in webCommands but runCommand never dispatches it", name)
		}
	}
	if len(commandSpecs) != len(webCommands) {
		t.Errorf("commandSpecs has %d entries for %d webCommands", len(commandSpecs), len(webCommands))
	}
	for name := range webDispatch {
		if known[name] {
			continue
		}
		if target := slash.Aliases[name]; target == "" || !known[target] {
			t.Errorf("%s is dispatched but is neither in webCommands nor an alias of one", name)
		}
	}
	for name := range webArgs {
		if !known[name] {
			t.Errorf("webArgs narrows %s, which is not in webCommands", name)
		}
	}
}

// TestAliasesObeyTheIdleGate: an alias of an idle-only command is
// refused mid-turn exactly as the canonical name is. The gate compared
// the typed name with commandSpecs, which carries canonical names only,
// so /new started a new session out from under a running turn while
// /clear was correctly refused.
func TestAliasesObeyTheIdleGate(t *testing.T) {
	if slash.Canonical("/new") != "/clear" {
		t.Fatal("/new should be an alias of /clear")
	}
	for _, name := range []string{"/clear", "/new"} {
		s := newServer(context.Background(), Options{})
		s.running = true
		err := s.runCommand(name)
		if err == nil || !strings.Contains(err.Error(), "waits for the turn to finish") {
			t.Errorf("%s during a running turn: err = %v, want a refusal", name, err)
		}
	}
}

// TestWebUndoTurnPreviewsThenApplies: the web UI gets "restore to before
// a turn" through the same shared parser and core the terminal uses —
// /undo turn n previews, /undo apply carries it out and tells the model.
func TestWebUndoTurnPreviewsThenApplies(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{
		Provider: &scripted{responses: []*llm.Response{
			{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockText, Text: "first"},
				{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"one.go","content":"package one\n"}`)},
			}}, StopReason: llm.StopToolUse},
			{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
			{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockText, Text: "second"},
				{Type: llm.BlockToolUse, ID: "w2", Name: "write_file", Input: json.RawMessage(`{"path":"two.go","content":"package two\n"}`)},
			}}, StopReason: llm.StopToolUse},
			{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
		}},
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Log: sess, AutoApprove: true,
	}
	s := newServer(context.Background(), Options{Agent: ag, Session: sess, Cwd: root, Version: "test"})
	for _, p := range []string{"write one", "write two"} {
		if err := ag.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	idle := func() {
		t.Helper()
		for i := 0; i < 500; i++ {
			s.mu.Lock()
			running := s.running
			s.mu.Unlock()
			if !running {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("the command never finished")
	}
	text := func() string {
		var sb strings.Builder
		for _, e := range s.snapshotEntries() {
			sb.WriteString(e.Name + " " + e.Text + "\n")
		}
		return sb.String()
	}

	if err := s.runCommand("/undo turn 2"); err != nil {
		t.Fatal(err)
	}
	idle()
	if out := text(); !strings.Contains(out, "before turn 2") || !strings.Contains(out, "two.go") {
		t.Fatalf("preview:\n%s", out)
	}
	// The preview says what leaves the conversation, not only what
	// happens to the files.
	if out := text(); !strings.Contains(out, "rewind this conversation") || !strings.Contains(out, "1 turn") {
		t.Fatalf("the preview does not say what it would drop:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(root, "two.go")); err != nil {
		t.Fatalf("the preview touched the tree: %v", err)
	}

	if err := s.runCommand("/undo apply"); err != nil {
		t.Fatal(err)
	}
	idle()
	if _, err := os.Stat(filepath.Join(root, "two.go")); !os.IsNotExist(err) {
		t.Fatalf("turn 2's file survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "one.go")); err != nil {
		t.Fatalf("turn 1's file went too: %v", err)
	}
	// The web UI gets the rewind too: turn 2 left the conversation with
	// its files, and one note stands where it was.
	var told bool
	for _, m := range ag.Messages {
		if strings.Contains(m.Text(), "this conversation was rewound") && strings.Contains(m.Text(), "two.go") {
			told = true
		}
	}
	if !told {
		t.Fatalf("the model was not told what the restore reverted: %+v", ag.Messages)
	}
	for _, m := range ag.Messages {
		if strings.Contains(m.Text(), "write two") {
			t.Fatal("turn 2's prompt is still in the context after a rewind")
		}
	}
	// A turn with no snapshot is refused, not half-applied.
	if err := s.runCommand("/undo turn 9"); err != nil {
		t.Fatal(err)
	}
	idle()
	if out := text(); !strings.Contains(out, "no snapshot for that turn") {
		t.Fatalf("no refusal for a turn that cannot be restored:\n%s", out)
	}
}

func TestUnknownWebCommandSuggestsTheNearest(t *testing.T) {
	s := newServer(context.Background(), Options{})
	for typed, want := range map[string]string{
		"/hlep":      "unknown command /hlep · did you mean /help?",
		"/frobnicat": "unknown command /frobnicat (/help lists them)",
	} {
		if err := s.runCommand(typed); err != nil {
			t.Fatal(err)
		}
		last := s.entries[len(s.entries)-1]
		if last.Text != want {
			t.Errorf("%s: %q, want %q", typed, last.Text, want)
		}
	}
}

// Stop cancels the HTTP request, which fails with a *url.Error wrapping
// context.Canceled. That drew a red failure card with a raw "Post ...:
// context canceled" and a Retry button.
func TestStopIsNotAFailure(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.running = true
	stop := &url.Error{Op: "Post", URL: "https://openrouter.ai/api/v1/chat/completions", Err: context.Canceled}
	s.finish(fmt.Errorf("turn: %w", stop))
	last := s.entries[len(s.entries)-1]
	if last.Error || !strings.HasPrefix(last.Text, "stopped after") {
		t.Fatalf("after Stop: %+v", last)
	}
}

func TestRoutineNoticeIsNotShownOnTheWeb(t *testing.T) {
	s := newServer(context.Background(), Options{})
	before := len(s.entries)
	s.apply(agent.Event{Kind: agent.EvNotice, Text: "repository map attached: 3 files"})
	if len(s.entries) != before {
		t.Fatalf("a routine notice was added: %+v", s.entries[len(s.entries)-1])
	}
}

// /open refuses what the terminal's /open refuses: a binary file, which
// it printed whole into the thread, and a line past the end, answered
// with a backwards range over nothing.
func TestOpenRefusesBinaryAndLinesPastTheEnd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("ELF\x00\x01\x02"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newServer(context.Background(), Options{Cwd: dir})
	if got := s.openText("a.bin"); !strings.Contains(got, "binary") || strings.Contains(got, "ELF") {
		t.Fatalf("/open a.bin = %q", got)
	}
	if got := s.openText("a.txt:99"); got != "a.txt has 3 lines" {
		t.Fatalf("/open a.txt:99 = %q", got)
	}
	if got := s.openText("a.txt:2"); !strings.Contains(got, "▶2 │ two") {
		t.Fatalf("/open a.txt:2 = %q", got)
	}
}

// A long tool result in a subagent's transcript is cut to its first 8 and
// last 4 lines, and the note between them counts the lines it hides: it
// said one fewer than it left out.
func TestChildLogCountsTheLinesItHides(t *testing.T) {
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	log := agent.ChildLog{
		ChildInfo: agent.ChildInfo{ID: "c1", Done: true},
		Messages: []llm.Message{
			{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"seq 30"}`)}}},
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "t1", Text: strings.Join(lines, "\n")}}},
		},
	}
	out := formatChildLog(log)
	if !strings.Contains(out, "line 8\n… 18 more lines\nline 27") {
		t.Fatalf("transcript:\n%s", out)
	}
}

// /connect asks the pages open now to show the connect form. Kept in the
// replay log, it reopened the form on every later page load and every
// reconnect, long after the provider was connected.
func TestConnectRequestIsNotReplayed(t *testing.T) {
	s := newServer(context.Background(), Options{})
	if err := s.runCommand("/connect"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.log {
		if e.Type == "connect" {
			t.Fatalf("connect envelope kept in the replay log: %+v", s.log)
		}
	}
}
