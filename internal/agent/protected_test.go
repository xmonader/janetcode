package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/tool"
)

// asks records what the approver was shown and denies everything, so a
// test can tell "asked" from "ran without asking".
func asks(seen *[]ToolCall) Approver {
	return ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
		*seen = append(*seen, c)
		return Deny, nil
	})
}

// protectedAgent is an agent with the real protected set, writing an
// ordinary file and a protected one.
func protectedAgent(t *testing.T, seen *[]ToolCall) (*Agent, string) {
	t.Helper()
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"new"}`),
		toolCall("t2", "write_file", `{"path":".npmrc","content":"//registry/:_authToken=x"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, asks(seen))
	a.Root = root
	a.Protected = perm.NewProtected(nil)
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})
	return a, root
}

func onlyProtected(t *testing.T, seen []ToolCall, root string) {
	t.Helper()
	if len(seen) != 1 {
		names := []string{}
		for _, c := range seen {
			names = append(names, c.Name+" "+string(c.Input))
		}
		t.Fatalf("approver asked %d times (%v), want once for the protected write", len(seen), names)
	}
	if !strings.Contains(seen[0].Warning, "protected path: ") || !strings.Contains(seen[0].Warning, ".npmrc") {
		t.Fatalf("warning = %q, want it to say which protected path", seen[0].Warning)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err != nil {
		t.Fatal("the ordinary write should have gone through without asking")
	}
	if _, err := os.Stat(filepath.Join(root, ".npmrc")); !os.IsNotExist(err) {
		t.Fatal("the protected write was refused, so the file must not exist")
	}
}

// Under /edits an ordinary edit goes through and a protected one asks.
func TestProtectedPathAsksUnderAutoEdits(t *testing.T) {
	var seen []ToolCall
	a, root := protectedAgent(t, &seen)
	a.AutoEdits = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	onlyProtected(t, seen, root)
}

// Under /auto the same holds.
func TestProtectedPathAsksUnderAutoMode(t *testing.T) {
	var seen []ToolCall
	a, root := protectedAgent(t, &seen)
	a.AutoMode = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	onlyProtected(t, seen, root)
}

// An allow rule for the whole tool does not cover a protected path.
func TestProtectedPathAsksWithAllowRule(t *testing.T) {
	var seen []ToolCall
	a, root := protectedAgent(t, &seen)
	a.Rules = perm.Rules{Allow: []perm.Rule{{Tool: "write_file"}}}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	onlyProtected(t, seen, root)
}

// --yolo asks too. A protected write is the one move whose consequences
// outlive the session, and the set is small enough that ordinary work
// never meets it.
func TestProtectedPathAsksUnderYolo(t *testing.T) {
	var seen []ToolCall
	a, root := protectedAgent(t, &seen)
	a.AutoApprove = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	onlyProtected(t, seen, root)
}

// A shell command that writes a protected path is caught the same way,
// through what the command would write rather than how it is spelled.
func TestProtectedPathAsksForShellWrite(t *testing.T) {
	var seen []ToolCall
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"cp payload .git/hooks/pre-commit"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, asks(&seen))
	a.Root = root
	a.Protected = perm.NewProtected(nil)
	a.Rules = perm.Rules{Allow: []perm.Rule{{Tool: "bash", Pattern: "cp *"}}}
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.Contains(seen[0].Warning, "protected path: ") {
		t.Fatalf("approver saw %+v, want one prompt naming the protected path", seen)
	}
}

// The hard deny tier refuses before anything can ask, and an allow rule
// does not reach it.
func TestHardDenyRefusesWithoutPrompting(t *testing.T) {
	var seen []ToolCall
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"rm -rf /"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, asks(&seen))
	a.Root = root
	a.AutoApprove = true
	a.Protected = perm.NewProtected(nil)
	a.Rules = perm.Rules{Allow: []perm.Rule{{Tool: "bash"}}}
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("a hard-denied command was put to the user: %+v", seen)
	}
	var result string
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "t1" {
				result = b.Text
			}
		}
	}
	if !strings.Contains(result, "Refused") || !strings.Contains(result, "never runs it") {
		t.Fatalf("tool result = %q, want the hard-deny refusal", result)
	}
}
