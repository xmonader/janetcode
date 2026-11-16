package webui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/evidence"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/tool"
)

// The verdict event brings the evidence model's line under the verdict,
// coloured by its status: a failed check is red, a stale one a caution.
func TestVerdictEventShowsTheEvidenceLine(t *testing.T) {
	s := newServer(context.Background(), Options{Cwd: "/work/proj"})
	failed := &evidence.Report{Status: evidence.Failed, Required: true, Blocks: true, Why: "go test ./...: failed (FAIL)",
		Checks: []evidence.Check{{Command: "go test ./...", Status: evidence.Failed, Exit: 1, Fresh: true}}}
	stale := &evidence.Report{Status: evidence.NotRun, Required: true, Blocks: true, Stale: true, Why: "every check predates the last edit (a.go)",
		Checks: []evidence.Check{{Command: "go test ./...", Status: evidence.Passed, Exit: 0}}}
	s.apply(agent.Event{Kind: agent.EvVerdict, Text: "✗ check failed: x", IsError: true, Evidence: failed})
	s.apply(agent.Event{Kind: agent.EvVerdict, Text: "⚠ unverified: y", Evidence: stale})
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []*entry
	for _, e := range s.entries {
		if e.Kind == "verdict" && strings.Contains(e.Text, "evidence:") {
			lines = append(lines, e)
		}
	}
	if len(lines) != 2 || lines[0].Tone != present.ToneBad || !strings.HasPrefix(lines[0].Text, "✗ evidence: failed") ||
		lines[1].Tone != present.ToneWarn || !strings.Contains(lines[1].Text, "(stale)") || !lines[1].Error {
		t.Fatalf("evidence lines = %+v %+v", lines[0], lines[1])
	}
}

// /status shows the session's verification evidence on the same model the
// turn line, the headless result and the proof notes use: code was
// edited and nothing checked it, so the final status is not_run and a
// success claim is blocked.
func TestStatusSheetShowsEvidence(t *testing.T) {
	s, root := planServer(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"a.go","content":"package m\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	)
	ag := s.opts.Agent
	ag.AutoApprove = true
	ag.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	if err := ag.Run(context.Background(), "write a.go", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.runCommand("/status"); err != nil {
		t.Fatal(err)
	}
	got := s.sheetText(t)
	for _, want := range []string{"evidence:", "not_run", "success claims blocked", "edited and no check ran"} {
		if !strings.Contains(got, want) {
			t.Fatalf("/status lacks %q:\n%s", want, got)
		}
	}
}
