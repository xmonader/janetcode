package minimal

import (
	"slices"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/prompt"
)

func build(t *testing.T, csv string) *agent.Agent {
	t.Helper()
	off, err := agent.ParseSwitches(csv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Model: "claude-sonnet-4-5", MaxIterations: 7, MaxTokens: 1234, CompactAt: 999}
	return New(t.TempDir(), cfg, nil, off, prompt.Override{}, t.TempDir())
}

// codeTool is the code-execution tool a real session would carry.
func codeTool(t *testing.T) string {
	if (&agent.Script{Parent: &agent.Agent{}, Root: t.TempDir()}).Available() {
		return "execute_code"
	}
	return "run_code"
}

// Evals and --what-if both run this agent, so the settings the two used to
// wire differently are pinned here: the context window (evals never set
// it), and execute_code only when its runtime is available, with run_code
// as the fallback (evals added execute_code unconditionally).
func TestNewDefault(t *testing.T) {
	ag := build(t, "")
	cfg := &config.Config{Model: "claude-sonnet-4-5"}
	if ag.ContextWindow != cfg.MetaFor(cfg.Model).ContextWindow {
		t.Fatalf("ContextWindow = %d, want the model's %d", ag.ContextWindow, cfg.MetaFor(cfg.Model).ContextWindow)
	}
	if ag.MaxIterations != 7 || ag.MaxTokens != 1234 || ag.CompactAt != 999 || ag.Model != "claude-sonnet-4-5" {
		t.Fatalf("limits not taken from config: %+v", ag)
	}
	if ag.Prepare == nil || ag.PlanCheck == nil || ag.Archive == nil || ag.Log == nil {
		t.Fatal("repo map turn prep, plan check, archive and log must all be wired")
	}
	names := ag.Tools.Names()
	for _, want := range []string{"read_file", "write_file", "edit_file", "bash", "list_dir", "glob", "grep", "expand_output", "todo", codeTool(t)} {
		if !slices.Contains(names, want) {
			t.Errorf("tools %v missing %s", names, want)
		}
	}
	if slices.Contains(names, "execute_code") && slices.Contains(names, "run_code") {
		t.Errorf("tools %v carry both code tools", names)
	}
}

func TestNewAblations(t *testing.T) {
	ag := build(t, "script, todo ,extratools,repomap,intent")
	want := []string{"bash", "edit_file", "read_file", "run_code", "write_file"}
	got := slices.Clone(ag.Tools.Names())
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	if ag.Prepare != nil {
		t.Fatal("repomap off must drop the turn prep")
	}
	if ag.Tools.Intent {
		t.Fatal("intent off must turn intent off")
	}
}

func TestParseSwitchesRejectsUnknown(t *testing.T) {
	off, err := agent.ParseSwitches("script,nope")
	if err == nil {
		t.Fatal("unknown switch accepted")
	}
	if !off["script"] || off["nope"] {
		t.Fatalf("off = %v, want the valid names only", off)
	}
}
