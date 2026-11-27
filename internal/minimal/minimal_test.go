package minimal

import (
	"os"
	"slices"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/tool"
)

func build(t *testing.T, csv string) *agent.Agent {
	t.Helper()
	off, err := agent.ParseSwitches(csv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Model: "claude-sonnet-4-5", MaxIterations: 7, MaxTokens: 1234, CompactAt: 999}
	ag, err := New(t.TempDir(), cfg, nil, off, prompt.Override{}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return ag
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
	if ag.Prepare == nil || ag.Archive == nil || ag.Log == nil {
		t.Fatal("repo map turn prep, archive and log must all be wired")
	}
	if ag.PlanCheck != nil {
		t.Fatal("plan check is opt-in (plan_check) and must be off by default")
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

// TestToolsWireProbeAndNudge guards the eval agent against the gap that
// made two A/B arms identical: bash and grep built without the probe and
// the hint a session gives them.
func TestToolsWireProbeAndNudge(t *testing.T) {
	for _, off := range []map[string]bool{{}, {"emptyprobe": true, "toolnudge": true}} {
		reg := tools(".", nil, nil, off, nil, nil)
		b, _ := reg.Get("bash")
		g, _ := reg.Get("grep")
		want := len(off) == 0
		if (b.(tool.Bash).Nudge != nil) != want || (b.(tool.Bash).Probe != nil) != want || (g.(tool.Grep).Probe != nil) != want {
			t.Errorf("off=%v: bash nudge/probe or grep probe wiring wrong", off)
		}
	}
}

// TestSandboxOffByDefault pins that an eval agent's bash is unsandboxed
// unless WithSandbox asks, so every result recorded before --eval-sandbox
// stays comparable.
func TestSandboxOffByDefault(t *testing.T) {
	ag := build(t, "")
	b, _ := ag.Tools.Get("bash")
	if bt := b.(tool.Bash); bt.Sandbox != nil || bt.WS != nil {
		t.Fatalf("bash is sandboxed by default: %+v", bt)
	}
}

func TestWithSandboxUnknownProfile(t *testing.T) {
	cfg := &config.Config{Model: "claude-sonnet-4-5"}
	if _, err := New(t.TempDir(), cfg, nil, nil, prompt.Override{}, t.TempDir(), nil, WithSandbox("nope")); err == nil {
		t.Fatal("unknown profile accepted")
	}
}

// TestSandboxStrict wires the strict profile and, where the machine has a
// backend, runs real commands: the work dir is writable, the network is
// not, and the go build and module caches are granted extra dirs.
func TestSandboxStrict(t *testing.T) {
	root := t.TempDir()
	sb, ws, err := Sandbox(root, "strict")
	if err != nil {
		t.Skipf("no sandbox backend here: %v", err)
	}
	if !sb.Enabled() || sb.ProfileName() != "strict" {
		t.Fatalf("sandbox = on:%v profile:%s", sb.Enabled(), sb.ProfileName())
	}
	for _, dir := range goDirs() {
		if !slices.Contains(ws.ExtraDirs(), dir) {
			t.Fatalf("%s missing from extra dirs %v", dir, ws.ExtraDirs())
		}
	}
	run := func(cmd string) error {
		c, err := sb.CommandIn(t.Context(), root, ws.ExtraDirs(), cmd)
		if err != nil {
			return err
		}
		return c.Run()
	}
	if err := run("echo ok > inside.txt"); err != nil {
		t.Fatalf("write in the work dir: %v", err)
	}
	if _, err := os.Stat(root + "/inside.txt"); err != nil {
		t.Fatalf("the write did not land: %v", err)
	}
	// No network under strict: the sandbox has no route out, so even a
	// connection to the loopback interface of the host is refused.
	if err := run("exec 3<>/dev/tcp/1.1.1.1/53"); err == nil {
		t.Fatal("strict let a command open a connection")
	}
}

func TestSandboxReadOnlyAddsNoGoCache(t *testing.T) {
	_, ws, err := Sandbox(t.TempDir(), "read-only")
	if err != nil {
		t.Skipf("no sandbox backend here: %v", err)
	}
	if len(ws.ExtraDirs()) != 0 {
		t.Fatalf("read-only has nothing writable but got extra dirs %v", ws.ExtraDirs())
	}
}
