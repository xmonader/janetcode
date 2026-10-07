package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/managed"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/tool"
)

// An ask rule puts a call to the user under --yolo and an allow rule,
// and even when the tool only reads — the .env read never happens
// without a person saying so.
func TestAskRuleForcesPromptForAReadUnderYolo(t *testing.T) {
	var seen []ToolCall
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "read_file", `{"path":"README.md"}`),
		toolCall("t2", "read_file", `{"path":".env"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, asks(&seen))
	a.Root = root
	os.WriteFile(filepath.Join(root, "README.md"), []byte("hi"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=x"), 0o644)
	a.Tools = tool.NewRegistry(tool.ReadFile{Root: root})
	rs, err := perm.File{Allow: []string{"read_file"}, Ask: []string{"*(touches:**/.env)"}}.Rules()
	if err != nil {
		t.Fatal(err)
	}
	a.Rules = rs
	a.AutoApprove = true
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !strings.Contains(string(seen[0].Input), ".env") {
		t.Fatalf("asked %d times (%v), want once, for .env", len(seen), seen)
	}
	w := seen[0].Warning
	if !strings.Contains(w, "asks because of rule *(touches:**/.env)") || !strings.Contains(w, "outranks allow rule read_file") {
		t.Errorf("warning = %q, want the rule that asked and the allow rule it outranked", w)
	}
}

// A deny names the allow rule it outranked, so the model and the user can
// see which rule won.
func TestDenyNamesOutrankedAllowRule(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"rm -f x"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, asks(new([]ToolCall)))
	a.Root = root
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	rs, _ := perm.File{Allow: []string{"bash(*)"}, Deny: []string{"bash(cmd:rm *)"}}.Rules()
	a.Rules = rs
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	out := toolResultText(a)
	if !strings.Contains(out, "Denied by permission rule bash(cmd:rm *) (it outranks allow rule bash(*))") {
		t.Errorf("tool result = %q", out)
	}
}

// A subagent inherits the parent's deny and ask rules.
func TestChildRulesKeepAskAndDeny(t *testing.T) {
	rs, _ := perm.File{Allow: []string{"bash"}, Deny: []string{"bash(rm *)"}, Ask: []string{"*(secret:*)"}}.Rules()
	c := childRules(rs)
	if len(c.Allow) != 0 || len(c.Deny) != 1 || len(c.Ask) != 1 {
		t.Fatalf("child rules = %+v", c)
	}
}

// A managed policy's disabled modes stay off however they are asked for.
func TestManagedPolicyClampsModes(t *testing.T) {
	a, _ := newAgent(t, &fake{}, asks(new([]ToolCall)))
	a.Policy = &managed.Policy{DisabledModes: []string{managed.ModeYolo, managed.ModeEdits}}
	a.UpdateSettings(func(s *Settings) { s.AutoApprove, s.AutoEdits, s.AutoMode = true, true, true })
	s := a.Settings()
	if s.AutoApprove || s.AutoEdits || !s.AutoMode {
		t.Fatalf("settings = yolo %v edits %v auto %v, want only auto on", s.AutoApprove, s.AutoEdits, s.AutoMode)
	}
	if a.ModeRefusal(managed.ModeYolo) == nil || a.ModeRefusal(managed.ModeAuto) != nil {
		t.Error("ModeRefusal disagrees with the policy")
	}
}

// Under managed_permission_rules_only a saved allow rule is refused and
// "always" at a prompt allows the one call.
func TestManagedRulesOnlyRefusesSavedAllow(t *testing.T) {
	a, _ := newAgent(t, &fake{}, asks(new([]ToolCall)))
	a.Policy = &managed.Policy{ManagedRulesOnly: true}
	r, _ := perm.Parse("bash(go test *)")
	if a.AddRule(perm.Allowed, r) {
		t.Fatal("an allow rule was added under managed_permission_rules_only")
	}
	if !a.AddRule(perm.Denied, r) {
		t.Fatal("a deny rule must still be addable")
	}
}

func toolResultText(a *Agent) string {
	var b strings.Builder
	for _, m := range a.MessagesSnapshot() {
		for _, bl := range m.Blocks {
			if bl.Type == llm.BlockToolResult {
				b.WriteString(bl.Text)
			}
		}
	}
	return b.String()
}
