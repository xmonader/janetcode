package webui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// tourResponses change two existing Go files in one turn.
func tourResponses() []*llm.Response {
	return []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Add instead."},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"a.go","content":"package m\n\nfunc F(x int) int { return x + 2 }\n"}`)},
			{Type: llm.BlockToolUse, ID: "w2", Name: "write_file", Input: json.RawMessage(`{"path":"b.go","content":"package m\n\nfunc G(x int) int { return x - 2 }\n"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}
}

func seedTourFiles(t *testing.T, root string) {
	t.Helper()
	for name, body := range map[string]string{
		"a.go": "package m\n\nfunc F(x int) int { return x * 2 }\n",
		"b.go": "package m\n\nfunc G(x int) int { return x * 2 }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// /tour renders the turn riskiest first, /changes carries the debt line
// until the tour has been read, and /stats carries it too.
func TestTourAndDebtSheets(t *testing.T) {
	s, root := planServer(t, tourResponses()...)
	seedTourFiles(t, root)
	ag := s.opts.Agent
	ag.AutoApprove = true
	ag.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := ag.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.runCommand("/changes"); err != nil {
		t.Fatal(err)
	}
	if got := s.sheetText(t); !strings.Contains(got, "comprehension debt: 4 lines approved unread · 4 in .") {
		t.Fatalf("/changes:\n%s", got)
	}
	if err := s.runCommand("/stats"); err != nil {
		t.Fatal(err)
	}
	if got := s.sheetText(t); !strings.Contains(got, "comprehension debt:") {
		t.Fatalf("/stats:\n%s", got)
	}
	if err := s.runCommand("/tour"); err != nil {
		t.Fatal(err)
	}
	got := s.sheetText(t)
	for _, want := range []string{"review tour of turn 1", "[behaviour] a.go", "[behaviour] b.go", "intent: Add instead."} {
		if !strings.Contains(got, want) {
			t.Fatalf("/tour lacks %q:\n%s", want, got)
		}
	}
	if err := s.runCommand("/changes"); err != nil {
		t.Fatal(err)
	}
	if got := s.sheetText(t); strings.Contains(got, "comprehension debt") {
		t.Fatalf("/changes after the tour still shows debt:\n%s", got)
	}
}

// Approving an edit on the page, whose card shows the diff whole, is
// reading it: that change is not debt. The one approved by rule is.
func TestApprovalWithPreviewIsNotDebt(t *testing.T) {
	s, root := planServer(t, tourResponses()...)
	seedTourFiles(t, root)
	ag := s.opts.Agent
	ag.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	h := s.handler()
	done := make(chan error, 1)
	go func() { done <- ag.Run(context.Background(), "go", nil) }()
	for range 2 {
		id := pendingID(t, s)
		dec := "allow"
		if id == "w2" {
			dec = "session" // approves w2 at its prompt too: still read
		}
		if code := postDecision(t, h, map[string]string{"decision": dec, "id": id}); code != 200 {
			t.Fatalf("answer %s: %d", id, code)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if line := ag.DebtLine(root); line != "" {
		t.Fatalf("debt after approving both at their cards = %q", line)
	}
}
