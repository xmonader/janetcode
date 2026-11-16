package webui

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// The workspace the web UI polls ties each changed file to the turn and
// the tool call that wrote it, and each check result to its turn and the
// calls it followed; the page renders both on the turn's file card.
func TestWorkspaceLinksFilesAndChecksToTurnAndCall(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	write := &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ID: "wr1", Name: "write_file", Input: json.RawMessage(`{"path":"out.txt","content":"x\n"}`)},
	}}, StopReason: llm.StopToolUse}
	done := &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}
	ag := &agent.Agent{Provider: &scripted{responses: []*llm.Response{write, done}}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}),
		Model: "m", Log: sess, Root: root, System: "s"}
	ag.AutoApprove = true
	s := newServer(context.Background(), Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root})
	if err := ag.Run(context.Background(), "write it", func(agent.Event) {}); err != nil {
		t.Fatal(err)
	}
	ag.LogEvent(session.Event{Type: session.TypeGate, Name: "go test ./...", Source: session.GateVerify})

	ws := s.workspace()
	if len(ws.Files) != 1 || ws.Files[0].Turn != 1 || ws.Files[0].Call != "wr1" {
		t.Fatalf("files = %+v, want out.txt from turn 1, call wr1", ws.Files)
	}
	if len(ws.Checks) != 1 || ws.Checks[0].Turn != 1 || ws.Checks[0].Failed || ws.Checks[0].Command != "go test ./..." || strings.Join(ws.Checks[0].Calls, ",") != "wr1" {
		t.Fatalf("checks = %+v", ws.Checks)
	}
	js, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-call="${esc(f.call)}"`, `data-testid="turn-check"`, "ws.checks"} {
		if !strings.Contains(string(js), want) && !strings.Contains(readStatic(t, "workspacepane.js"), want) {
			t.Errorf("the page does not render %q", want)
		}
	}
}

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("static/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
