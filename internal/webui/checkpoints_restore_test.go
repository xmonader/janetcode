package webui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// twoTurnServer runs two turns (one.go, then two.go) and returns a web
// server over the agent plus a settle func that waits out a command.
func twoTurnServer(t *testing.T) (s *server, root string, settle func(), text func() string) {
	t.Helper()
	root = t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	write := func(id, path, body string) *llm.Response {
		in, _ := json.Marshal(map[string]string{"path": path, "content": body})
		return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: "write_file", Input: in}}}, StopReason: llm.StopToolUse}
	}
	done := &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}
	ag := &agent.Agent{
		Provider: &scripted{responses: []*llm.Response{write("w1", "one.go", "package one\n"), done, write("w2", "two.go", "package two\n"), done}},
		Tools:    tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Log: sess, Root: root, AutoApprove: true,
	}
	s = newServer(context.Background(), Options{Agent: ag, Session: sess, Cwd: root, Version: "test"})
	for _, p := range []string{"write one", "write two"} {
		if err := ag.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	settle = func() {
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
	text = func() string {
		var sb strings.Builder
		for _, e := range s.snapshotEntries() {
			sb.WriteString(e.Name + " " + e.Text + "\n")
		}
		return sb.String()
	}
	return s, root, settle, text
}

// The web restore refuses a file edited since the turn, says which and how
// to override, leaves everything as it was, and goes through when forced.
func TestWebRestoreRefusesEditedFilesUnlessForced(t *testing.T) {
	s, root, settle, text := twoTurnServer(t)
	two := filepath.Join(root, "two.go")
	if err := os.WriteFile(two, []byte("package two // edited by hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(cmd string) {
		t.Helper()
		if err := s.runCommand(cmd); err != nil {
			t.Fatal(err)
		}
		settle()
	}
	run("/undo turn 2")
	if out := text(); !strings.Contains(out, "CONFLICT") || !strings.Contains(out, "two.go") || !strings.Contains(out, "--force") {
		t.Fatalf("the preview must flag the edited file:\n%s", out)
	}
	run("/undo apply")
	if out := text(); !strings.Contains(out, "refusing to overwrite files that changed") || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("no refusal:\n%s", out)
	}
	if data, _ := os.ReadFile(two); !strings.Contains(string(data), "edited by hand") {
		t.Fatalf("a refused restore changed the file: %q", data)
	}
	run("/undo apply --force")
	if _, err := os.Stat(two); !os.IsNotExist(err) {
		t.Fatalf("the forced restore left two.go: %v", err)
	}
	if out := text(); !strings.Contains(out, "forced over") {
		t.Fatalf("the report does not say what was forced:\n%s", out)
	}
}

// /checkpoints diff and export work from the web, and commit only
// previews until it is confirmed. Outside a repository it says so
// instead of committing anything.
func TestWebCheckpointsDiffExportCommit(t *testing.T) {
	s, root, settle, text := twoTurnServer(t)
	run := func(cmd string) {
		t.Helper()
		if err := s.runCommand(cmd); err != nil {
			t.Fatal(err)
		}
		settle()
	}
	run("/checkpoints diff 1 now")
	if out := text(); !strings.Contains(out, "one.go") || !strings.Contains(out, "two.go") || !strings.Contains(out, "+package two") {
		t.Fatalf("diff:\n%s", out)
	}
	run("/checkpoints export 2")
	data, err := os.ReadFile(filepath.Join(root, "janetcode-checkpoint-t2.patch"))
	if err != nil || !strings.Contains(string(data), "+package two") {
		t.Fatalf("export wrote %q, %v", data, err)
	}
	run("/checkpoints commit 2")
	if out := text(); !strings.Contains(out, "git repository") && !strings.Contains(out, "attribution") {
		t.Fatalf("commit must either preview with attribution or say there is no repository:\n%s", out)
	}
	run("/checkpoints bogus")
	if out := text(); !strings.Contains(out, "unknown checkpoints action") {
		t.Fatalf("a bad verb:\n%s", out)
	}
}
