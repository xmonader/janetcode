package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// TestImpactShownOnlyWhenAsking: the Impact hook runs exactly when a
// permission prompt is about to be shown for an edit — never under
// auto-approve, where the call is never asked about at all — and its
// answer reaches the call the approver sees.
func TestImpactShownOnlyWhenAsking(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := 0
	impactFn := func(ctx context.Context, call ToolCall) string {
		calls++
		return "changes Foo (func) · 1 ref in 1 file"
	}

	var seen ToolCall
	approver := ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		seen = call
		return Allow, nil
	})
	f := &fake{responses: []*llm.Response{
		toolCall("e1", "edit_file", `{"path":"a.go","old_string":"package a","new_string":"package a // x"}`),
		text("done"),
	}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(tool.EditFile{Root: root}), Approver: approver, Impact: impactFn, Model: "m"}
	if err := a.Run(context.Background(), "edit", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("Impact called %d times while asking, want 1", calls)
	}
	if seen.Impact == "" {
		t.Fatal("the call the approver saw has no Impact, though the hook ran")
	}

	// Under auto-approve nothing is ever asked, so the hook — whose only
	// purpose is to feed a prompt — must not run either.
	calls = 0
	f2 := &fake{responses: []*llm.Response{
		toolCall("e2", "edit_file", `{"path":"a.go","old_string":"package a // x","new_string":"package a // y"}`),
		text("done"),
	}}
	a2 := &Agent{Provider: f2, Tools: tool.NewRegistry(tool.EditFile{Root: root}), AutoApprove: true, Impact: impactFn, Model: "m"}
	if err := a2.Run(context.Background(), "edit", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("Impact called %d times under auto-approve, want 0", calls)
	}
}

// TestImpactNotAskedForNonEditTools: the hook is specific to edit_file and
// write_file; a mutating call of another shape must not trigger it.
func TestImpactNotAskedForNonEditTools(t *testing.T) {
	root := t.TempDir()
	calls := 0
	impactFn := func(ctx context.Context, call ToolCall) string {
		calls++
		return "should not be called"
	}
	approver := ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) { return Allow, nil })
	f := &fake{responses: []*llm.Response{
		toolCall("b1", "bash", `{"command":"echo hi"}`),
		text("done"),
	}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(tool.Bash{Root: root}), Approver: approver, Impact: impactFn, Model: "m"}
	if err := a.Run(context.Background(), "run it", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("Impact called %d times for a bash call, want 0", calls)
	}
}
