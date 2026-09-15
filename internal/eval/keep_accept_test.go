package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/verify"
)

// acceptSession is keptSession for a session that ran /accept: its
// acceptance test file is locked with the given command.
func acceptSession(t *testing.T, root, command string, prompts ...string) *session.Session {
	t.Helper()
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "test session start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, prompts...)
	test := filepath.Join(root, "accept", "widget_test.sh")
	acc := &session.AcceptMeta{
		Phase:    session.AcceptLocked,
		Request:  prompts[0],
		Root:     root,
		Files:    []string{test},
		Contents: map[string][]byte{test: []byte("grep -q done widget.txt\n")},
		Command:  "sh accept/widget_test.sh",
	}
	if command != "" {
		acc.Command = command
	}
	if err := sess.SetAccept(acc); err != nil {
		t.Fatal(err)
	}
	return sess
}

// TestKeepIncludesLockedAcceptanceTests is the regression test for a kept
// eval that could not fail: the check was verify.json's command alone,
// which already passed at the base commit, and the session's locked
// acceptance tests were left out. The kept task now carries the tests as
// locked files and runs their command.
func TestKeepIncludesLockedAcceptanceTests(t *testing.T) {
	root := initKeepRepo(t)
	if err := verify.Save(root, verify.Config{Commands: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	sess := acceptSession(t, root, "", "make the widget say done")
	_, task, _, err := Keep(KeepOptions{Root: root, Session: sess, Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if got := task.Files["accept/widget_test.sh"]; got != "grep -q done widget.txt\n" {
		t.Fatalf("Files = %v, want the locked acceptance test", task.Files)
	}
	if len(task.Locked) != 1 || task.Locked[0] != "accept/widget_test.sh" {
		t.Fatalf("Locked = %v", task.Locked)
	}
	if task.Check != "sh accept/widget_test.sh && true" {
		t.Fatalf("Check = %q, want the accept command, then verify.json's", task.Check)
	}
	if !strings.Contains(task.Prompts[0], "accept/widget_test.sh") {
		t.Fatalf("first prompt does not say the acceptance tests exist: %q", task.Prompts[0])
	}
	passed, err := CheckAtBase(context.Background(), task)
	if err != nil {
		t.Fatalf("CheckAtBase: %v", err)
	}
	if passed {
		t.Fatalf("the kept check passes at the base commit, so the eval cannot fail")
	}
}

// TestKeepCommandWarnsWhenCheckPassesAtBase: a check that already passes
// before any work proves nothing, and /eval keep says so (without
// refusing to keep the task).
func TestKeepCommandWarnsWhenCheckPassesAtBase(t *testing.T) {
	root := initKeepRepo(t)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "test session start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, "add a widget")
	res, err := KeepCommand(context.Background(), KeepOptions{Root: root, Session: sess}, "keep --check true")
	if err != nil {
		t.Fatalf("KeepCommand: %v", err)
	}
	if !strings.Contains(strings.Join(res.Tips, "\n"), "passes before any work") {
		t.Fatalf("Tips = %v, want the cannot-fail warning", res.Tips)
	}
}

// TestKeepNotesDroppedDenialReasons: a reason the user gave when denying
// a tool call is mid-turn guidance a task's per-turn prompts cannot carry,
// so /eval keep says it was left out rather than dropping it silently.
func TestKeepNotesDroppedDenialReasons(t *testing.T) {
	root := initKeepRepo(t)
	repo, err := snapshot.Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.Take(context.Background(), "test session start")
	if err != nil {
		t.Fatal(err)
	}
	sess := keptSession(t, root, snap, "add a widget")
	tr := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "c1", Text: agent.DeniedMessage + " The user said: use the existing helper"}}}
	if _, err := sess.Append(session.Event{Type: session.TypeToolResult, Turn: 1, Surface: true, Message: &tr}); err != nil {
		t.Fatal(err)
	}
	_, _, warnings, err := Keep(KeepOptions{Root: root, Session: sess, Check: "true", Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("Keep: %v", err)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "1 denial reason") {
		t.Fatalf("warnings = %v, want a note about the dropped denial reason", warnings)
	}
}

// TestUserPromptsSkipsGluedHarnessNote: a harness note added between
// turns (the /undo notice) merges into the next prompt's message; the
// kept prompt is the typed text only.
func TestUserPromptsSkipsGluedHarnessNote(t *testing.T) {
	msg := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{
		{Type: llm.BlockText, Text: "I undid some of my earlier changes based on your description:\na.go", Synthetic: true},
		{Type: llm.BlockText, Text: "now rename the widget"},
	}}
	first := llm.TextMessage(llm.RoleUser, "add a widget")
	events := []session.Event{
		{Type: session.TypeUserMessage, Turn: 1, Message: &first},
		{Type: session.TypeUserMessage, Turn: 2, Message: &msg},
	}
	got := userPrompts(events)
	if len(got) != 2 || got[1] != "now rename the widget" {
		t.Fatalf("userPrompts = %q", got)
	}
}

// TestLockedFilesRestoredBeforeCheck: the files a task locks are written
// again, from Files, just before its check runs, so a run cannot pass by
// weakening them.
func TestLockedFilesRestoredBeforeCheck(t *testing.T) {
	dir := t.TempDir()
	task := Task{Files: map[string]string{"t.sh": "exit 1\n", "free.txt": "a"}, Locked: []string{"t.sh"}}
	if err := os.WriteFile(filepath.Join(dir, "t.sh"), []byte("exit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "free.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restoreLocked(dir, task); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "t.sh")); string(b) != "exit 1\n" {
		t.Fatalf("t.sh = %q, want restored", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "free.txt")); string(b) != "b" {
		t.Fatalf("free.txt = %q, want the run's edit kept", b)
	}
}
