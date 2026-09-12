package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestRunCodeCallsRealToolAndDenialStillDenies(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ag := &Agent{
		Tools:       tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root}),
		Log:         &session.Memory{},
		AutoApprove: true,
	}
	rc := &RunCode{Parent: ag}
	ag.Tools.Add(rc)
	out, err := rc.Run(context.Background(), json.RawMessage(`{"description":"read it","code":[{"name":"read_file","input":{"path":"a.txt"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "1 tool call") {
		t.Fatalf("out = %q", out)
	}
	n := 0
	for _, e := range ag.Log.Events() {
		if e.Type == session.TypeToolCall && e.Name == "read_file" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("nested tool calls = %d", n)
	}

	asked := 0
	deny := ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
		asked++
		return Deny, nil
	})
	blocked := &Agent{
		Tools:    tool.NewRegistry(tool.WriteFile{Root: root}),
		Log:      &session.Memory{},
		Approver: deny,
	}
	rc2 := &RunCode{Parent: blocked}
	blocked.Tools.Add(rc2)
	got, err := rc2.Run(context.Background(), json.RawMessage(`{"code":[{"name":"write_file","input":{"path":"x.txt","content":"no"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("approver asked %d times", asked)
	}
	if !strings.Contains(got, "denied") && !strings.Contains(strings.ToLower(got), "permission") && !strings.Contains(got, "Denied") && !strings.Contains(got, DeniedMessage) {
		t.Fatalf("denial missing from %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); err == nil {
		t.Fatal("denied write_file still wrote the file")
	}
}

func TestRunCodeRefusesNestedTask(t *testing.T) {
	ag := &Agent{Tools: tool.NewRegistry(), Log: &session.Memory{}, AutoApprove: true}
	rc := &RunCode{Parent: ag}
	ag.Tools.Add(rc)
	_, err := rc.Run(context.Background(), json.RawMessage(`{"code":[{"name":"task","input":{"description":"x","prompt":"y"}}]}`))
	if err == nil || !strings.Contains(err.Error(), "cannot be called") {
		t.Fatalf("err = %v", err)
	}
}
