package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// A script's edit_file call carries then_run like the model's does: the
// command runs after the edit and the post-tool hook fires for the call.
func TestNestedCallRunsThenRunAndPostToolHook(t *testing.T) {
	f := &fake{}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root, ThenRun: true})
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.PostTool: {{Match: "write_file", Command: `echo '{"note":"post hook ran"}'`}},
	}}}
	out, isErr, _ := a.runNested(context.Background(), "s1", "write_file", []byte(`{"path":"n.txt","content":"v1","then_run":"cat n.txt; echo checked"}`))
	if isErr {
		t.Fatalf("nested call failed: %q", out)
	}
	if !strings.Contains(out, "[then_run] $ cat n.txt; echo checked") || !strings.Contains(out, "v1checked") {
		t.Fatalf("then_run did not run for a nested call: %q", out)
	}
	if !strings.Contains(out, "post hook ran") {
		t.Fatalf("the post-tool hook did not fire for a nested call: %q", out)
	}
}

// namesApprover records every call it is asked about and refuses the
// named tool.
type namesApprover struct {
	asked []string
	deny  string
}

func (n *namesApprover) Approve(_ context.Context, c ToolCall) (Decision, error) {
	n.asked = append(n.asked, c.Name+" "+string(c.Input))
	if c.Name == n.deny {
		return Deny, nil
	}
	return Allow, nil
}

func thenRunResult(t *testing.T, a *Agent) string {
	t.Helper()
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				return b.Text
			}
		}
	}
	t.Fatal("no tool result in the history")
	return ""
}

// An edit carrying then_run lands, then its command runs as a bash call
// and its output joins the edit's result: one round trip, not two.
func TestThenRunRunsAfterTheEdit(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"v1","then_run":"cat x.txt; echo checked"}`),
		text("done"),
	}}
	ap := &namesApprover{}
	a, root := newAgent(t, f, ap)
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root, ThenRun: true})
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "x.txt")); string(got) != "v1" {
		t.Fatalf("x.txt = %q", got)
	}
	out := thenRunResult(t, a)
	if !strings.Contains(out, "[then_run] $ cat x.txt; echo checked") || !strings.Contains(out, "v1checked") {
		t.Fatalf("result = %q", out)
	}
	// The command was put to the approver as a bash call of its own, and
	// the edit was asked about without the field.
	if len(ap.asked) != 2 || !strings.HasPrefix(ap.asked[0], "write_file ") || strings.Contains(ap.asked[0], "then_run") || !strings.HasPrefix(ap.asked[1], "bash ") {
		t.Fatalf("asked = %q", ap.asked)
	}
}

// A refused command leaves the edit standing and says it was refused.
func TestThenRunRefusedKeepsTheEdit(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"v1","then_run":"echo should-not-run"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, &namesApprover{deny: "bash"})
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root, ThenRun: true})
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "x.txt")); string(got) != "v1" {
		t.Fatalf("x.txt = %q", got)
	}
	out := thenRunResult(t, a)
	if !strings.Contains(out, "[then_run] $ echo should-not-run\nThe user denied permission") {
		t.Fatalf("result = %q", out)
	}
}

// An edit that fails runs nothing.
func TestThenRunSkippedWhenTheEditFails(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "edit_file", `{"path":"missing.txt","old_string":"a","new_string":"b","then_run":"touch ran"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, &namesApprover{})
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.EditFile{Root: root, ThenRun: true})
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "ran")); err == nil {
		t.Fatal("then_run ran after a failed edit")
	}
	if out := thenRunResult(t, a); !strings.Contains(out, "[then_run not run: the edit failed]") {
		t.Fatalf("result = %q", out)
	}
}
