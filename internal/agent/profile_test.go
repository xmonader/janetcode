package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentprofile "github.com/xmonader/janetcode/internal/skill/profile"
	"github.com/xmonader/janetcode/internal/tool"
)

// A profile's denylist is applied after the allowlist: a tool the
// profile refuses cannot be let back in by naming it.
func TestProfileDisallowedToolsWinOverTheAllowlist(t *testing.T) {
	root := t.TempDir()
	parent := &Agent{Tools: tool.NewRegistry(tool.ReadFile{Root: root}, tool.Grep{Root: root}, tool.ListDir{Root: root})}
	task := &Task{Parent: parent}
	parent.Tools.Add(task)
	profile := &agentprofile.Profile{Name: "reader", Disallowed: []string{"grep"}}
	child, err := task.child([]string{"grep", "read_file"}, profile)
	if err != nil {
		t.Fatal(err)
	}
	defs := child.Tools.Defs()
	if len(defs) != 1 || defs[0].Name != "read_file" {
		t.Fatalf("child tools = %+v", defs)
	}
	// Denying everything it was given leaves nothing to work with, and
	// says so rather than starting a subagent with no tools.
	if _, err := task.child([]string{"grep"}, &agentprofile.Profile{Name: "empty", Disallowed: []string{"grep"}}); err == nil {
		t.Fatal("a subagent with no tools left was accepted")
	}
}

func TestProfileCapsTurns(t *testing.T) {
	root := t.TempDir()
	parent := &Agent{Tools: tool.NewRegistry(tool.ReadFile{Root: root})}
	task := &Task{Parent: parent}
	child, err := task.child(nil, &agentprofile.Profile{Name: "brief", MaxTurns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if child.MaxIterations != 3 {
		t.Fatalf("MaxIterations = %d", child.MaxIterations)
	}
	plain, err := task.child(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.MaxIterations != 25 {
		t.Fatalf("default MaxIterations = %d", plain.MaxIterations)
	}
}

// A confirmation a subagent needs is queued to the parent's approver
// rather than answered by the subagent for itself.
func TestBubbledApprovalReachesTheParentApprover(t *testing.T) {
	var got ToolCall
	parent := ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
		got = c
		return Allow, nil
	})
	b := bubbled{to: parent, name: "fixer"}
	if d, err := b.Approve(context.Background(), ToolCall{Name: "bash", Input: json.RawMessage(`{}`)}); d != Allow || err != nil {
		t.Fatalf("decision = %v, %v", d, err)
	}
	if !strings.Contains(got.Intent, "fixer") {
		t.Fatalf("the prompt does not say who is asking: %q", got.Intent)
	}
	if _, err := b.Approve(context.Background(), ToolCall{Name: "bash", Intent: "run the tests"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Intent, "fixer") || !strings.Contains(got.Intent, "run the tests") {
		t.Fatalf("intent = %q", got.Intent)
	}
}

func TestProfileParsesBudgetsAndApproval(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, ".janetcode", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: fixer\ndescription: fixes things\ntools: read_file, bash\ndisallowed_tools: bash\nmax_turns: 8\nmax_minutes: 3\napproval: bubble\n---\nFix it.\n"
	if err := os.WriteFile(filepath.Join(agents, "fixer.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	ps := agentprofile.LoadProfiles(dir, "")
	if len(ps) != 1 {
		t.Fatalf("profiles = %+v", ps)
	}
	p := ps[0]
	if len(p.Disallowed) != 1 || p.Disallowed[0] != "bash" {
		t.Fatalf("disallowed = %v", p.Disallowed)
	}
	if p.MaxTurns != 8 || p.MaxMinutes != 3 || !p.Bubble() {
		t.Fatalf("profile = %+v", p)
	}
	// A budget nobody can parse is not a budget.
	body = "---\nname: loose\nmax_turns: soon\napproval: no\n---\nGo.\n"
	if err := os.WriteFile(filepath.Join(agents, "loose.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range agentprofile.LoadProfiles(dir, "") {
		if p.Name == "loose" && (p.MaxTurns != 0 || p.Bubble()) {
			t.Fatalf("profile = %+v", p)
		}
	}
}
