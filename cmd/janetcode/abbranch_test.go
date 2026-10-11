package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// abBranchLive is the live model after the branch point: it answers once
// and records that it was asked.
type abBranchLive struct{ asked int }

func (p *abBranchLive) Name() string { return "live" }
func (p *abBranchLive) Stream(context.Context, llm.Request, func(llm.Event)) (*llm.Response, error) {
	p.asked++
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "live done"), StopReason: llm.StopEndTurn}, nil
}

func abBranchRecording(t *testing.T, oldDir string) string {
	t.Helper()
	rec := `{"type":"meta","meta":{"id":"s1","cwd":"` + oldDir + `"}}
{"type":"event","seq":1,"t":"assistant/message","turn":1,"message":{"role":"assistant","blocks":[{"type":"tool_use","id":"c1","name":"write_file","input":{"path":"` + oldDir + `/out.txt","content":"from the recording"}}]}}
{"type":"event","seq":2,"t":"assistant/message","turn":1,"message":{"role":"assistant","blocks":[{"type":"text","text":"recorded done"}]}}
`
	path := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(path, []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestABBranchReplaysIntoThisWorkspaceThenGoesLive: the recorded first
// call writes into the recording's workspace by absolute path; replayed
// here, it must land in this run's workspace, and the call after the
// branch point must go to the live model, not the recording.
func TestABBranchReplaysIntoThisWorkspaceThenGoesLive(t *testing.T) {
	oldDir, newDir := filepath.Join(t.TempDir(), "old"), t.TempDir()
	path := abBranchRecording(t, oldDir)
	live := &abBranchLive{}
	ag := &agent.Agent{Provider: live, Tools: tool.NewRegistry(tool.WriteFile{Root: newDir}), Model: "m", AutoApprove: true}
	if err := abBranch(ag, path, 1, newDir); err != nil {
		t.Fatal(err)
	}
	if err := ag.Run(context.Background(), "do it", nil); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(newDir, "out.txt")); err != nil || string(b) != "from the recording" {
		t.Fatalf("replayed write did not land in this workspace: %q %v", b, err)
	}
	if _, err := os.Stat(oldDir); err == nil {
		t.Fatal("the replay wrote into the recording's own directory")
	}
	if live.asked != 1 {
		t.Fatalf("live model asked %d times, want 1 (the call after the branch point)", live.asked)
	}
}

func TestABBranchRefusesWhatCannotBranch(t *testing.T) {
	path := abBranchRecording(t, "/old")
	ag := &agent.Agent{Provider: &abBranchLive{}, Model: "m"}
	for _, c := range []struct {
		calls int
		want  string
	}{
		{0, "--ab-replay-calls N"},
		{2, "nothing would run live"},
	} {
		if err := abBranch(ag, path, c.calls, "/new"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("calls=%d: %v", c.calls, err)
		}
	}
	if err := abBranch(ag, "", 3, "/new"); err != nil {
		t.Fatalf("no --ab-replay is a no-op: %v", err)
	}
}
