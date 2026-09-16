package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// The sandbox profile is settled by two inputs that can disagree: a
// config key naming a profile and a --sandbox flag asking for one
// without naming it. "off" is the explicit opt-out, and it has to beat
// the flag -- otherwise a project that turned the sandbox off gets one
// anyway from a habit in someone's shell alias.
func TestSandboxProfile(t *testing.T) {
	for _, c := range []struct {
		configured string
		flag       bool
		want       string
	}{
		{"", false, ""},
		{"", true, shell.DefaultProfile},
		{"off", false, ""},
		{"off", true, ""},
		{"docker", false, "docker"},
		{"docker", true, "docker"},
	} {
		if got := sandboxProfile(c.configured, c.flag); got != c.want {
			t.Errorf("sandboxProfile(%q, %v) = %q, want %q", c.configured, c.flag, got, c.want)
		}
	}
}

// buildTestDeps is the smallest wiring a step needs: a fake provider, a
// throwaway config directory and a project directory of its own.
func buildTestDeps(t *testing.T) (agentDeps, *session.Session) {
	t.Helper()
	cwd := t.TempDir()
	sessDir := t.TempDir()
	sess, err := session.New(sessDir, cwd, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	return agentDeps{
		prov:    &fakeResumeProvider{},
		cfg:     &config.Config{Model: "m"},
		cwd:     cwd,
		cfgDir:  t.TempDir(),
		sessDir: sessDir,
		mem:     memory.Open(t.TempDir(), cwd),
	}, sess
}

// Every step has to be named, and the names have to be distinct: a
// failure is reported as "<step>: <what went wrong>", so a duplicate
// name points at two different places at once.
func TestBuildStepsAreNamedAndDistinct(t *testing.T) {
	d, sess := buildTestDeps(t)
	b := newAgentBuilder(d, sess, nil)
	seen := map[string]bool{}
	for i, step := range b.buildSteps() {
		if step.name == "" {
			t.Errorf("step %d has no name", i)
		}
		if seen[step.name] {
			t.Errorf("two steps are both called %q", step.name)
		}
		seen[step.name] = true
		if step.fn == nil {
			t.Errorf("step %q has no function", step.name)
		}
	}
	if len(seen) < 5 {
		t.Fatalf("only %d steps; the build was meant to be broken up", len(seen))
	}
}

// A directory --add-dir names is the user asking for it right now, so a
// bad one stops the build and says which step refused. A directory the
// session merely remembers may have been deleted since, and is dropped.
func TestStepWorkspaceAddDir(t *testing.T) {
	d, sess := buildTestDeps(t)
	extra := t.TempDir()
	d.extraDirs = []string{extra}
	b := newAgentBuilder(d, sess, nil)
	if err := b.stepWorkspace(); err != nil {
		t.Fatalf("stepWorkspace: %v", err)
	}
	if got := b.ws.ExtraDirs(); len(got) != 1 || got[0] != extra {
		t.Fatalf("extra dirs = %v, want [%s]", got, extra)
	}

	d2, sess2 := buildTestDeps(t)
	d2.extraDirs = []string{filepath.Join(t.TempDir(), "nope")}
	b2 := newAgentBuilder(d2, sess2, nil)
	if err := b2.stepWorkspace(); err == nil {
		t.Fatal("a missing --add-dir must fail the build")
	}

	// One the session remembers but that has since gone is not an error:
	// a resumed session must still open.
	d3, sess3 := buildTestDeps(t)
	sess3.Meta.ExtraDirs = []string{filepath.Join(t.TempDir(), "gone")}
	b3 := newAgentBuilder(d3, sess3, nil)
	if err := b3.stepWorkspace(); err != nil {
		t.Fatalf("a remembered directory that vanished must not fail the build: %v", err)
	}
}

// A loop spec the session recorded but that no longer parses is dropped
// by Restore, not fatal: like a remembered directory that vanished, it
// must not keep a resumed session from opening.
func TestStepLoopsToleratesABadSpec(t *testing.T) {
	d, sess := buildTestDeps(t)
	sess.Meta.Loops = []session.LoopSpec{{ID: "x", Every: "not a duration", Prompt: "go"}}
	if _, err := buildAgent(d, sess, nil); err != nil {
		t.Fatalf("a loop spec that no longer parses must not fail the build: %v", err)
	}
}

// JANETCODE_OFF is read once for the whole build. It used to be read on
// each use, so a session could in principle disagree with itself about
// what is switched off; more practically, the one reading is what the
// agent reports as its own ablation state.
func TestOffIsReadOncePerBuild(t *testing.T) {
	t.Setenv("JANETCODE_OFF", "script,intent")
	d, sess := buildTestDeps(t)
	b := newAgentBuilder(d, sess, nil)
	if !b.off["script"] || !b.off["intent"] {
		t.Fatalf("off = %v, want script and intent switched off", b.off)
	}
	// Changing it mid-build changes nothing: the build has its reading.
	t.Setenv("JANETCODE_OFF", "")
	if !b.off["script"] {
		t.Fatal("the build re-read JANETCODE_OFF")
	}
	if err := b.stepSandbox(); err != nil {
		t.Fatal(err)
	}
	if err := b.stepWorkspace(); err != nil {
		t.Fatal(err)
	}
	if err := b.stepLoops(); err != nil {
		t.Fatal(err)
	}
	if err := b.stepCore(); err != nil {
		t.Fatal(err)
	}
	if err := b.stepAgentTools(); err != nil {
		t.Fatal(err)
	}
	// execute_code and run_code are one feature: with script off, the
	// Python-less form stands in and the model is never offered both.
	if b.script != nil {
		t.Fatal("script was added although JANETCODE_OFF names it")
	}
	names := toolNames(b.ag)
	if names["execute_code"] {
		t.Fatalf("execute_code offered with script off: %v", names)
	}
	if !names["run_code"] {
		t.Fatalf("run_code did not stand in for execute_code: %v", names)
	}
}

// lean_tools moves the search surface behind tool_search rather than
// removing it: the tools stay reachable, they just stop costing prefix.
func TestStepSearchToolsLeanDefersRatherThanDrops(t *testing.T) {
	build := func(lean bool) map[string]bool {
		t.Helper()
		d, sess := buildTestDeps(t)
		d.cfg.LeanTools_ = &lean
		b := newAgentBuilder(d, sess, nil)
		for _, step := range []func() error{b.stepSandbox, b.stepWorkspace, b.stepLoops, b.stepCore, b.stepSearchTools} {
			if err := step(); err != nil {
				t.Fatal(err)
			}
		}
		return toolNames(b.ag)
	}
	full, lean := build(false), build(true)
	for _, name := range []string{"list_dir", "glob", "grep", "repo_map"} {
		if !full[name] {
			t.Errorf("%s is not offered without lean_tools", name)
		}
		if lean[name] {
			t.Errorf("%s is still in the prefix with lean_tools on", name)
		}
	}
	if !lean["tool_search"] {
		t.Error("lean_tools removed the search tools without leaving tool_search to find them")
	}
}

// toolNames is the set of tools the model is offered up front (deferred
// ones are reachable through tool_search and are not in the prefix).
func toolNames(ag *agent.Agent) map[string]bool {
	out := map[string]bool{}
	for _, n := range ag.Tools.Names() {
		out[n] = true
	}
	return out
}

// The build has to leave nothing half-wired behind when a step fails:
// the caller gets a zero builtAgent, whose close is a no-op.
func TestBuildAgentFailureReturnsNothingToClose(t *testing.T) {
	d, sess := buildTestDeps(t)
	d.extraDirs = []string{filepath.Join(t.TempDir(), "missing")}
	b, err := buildAgent(d, sess, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.HasPrefix(err.Error(), "workspace: ") {
		t.Fatalf("err = %v, want it prefixed with the step name", err)
	}
	if b.agent != nil || b.cd != nil {
		t.Fatal("a failed build handed back a partly wired agent")
	}
	b.close() // must not panic on the zero value
}

// A full build still produces what the front-ends read off it. This is
// the seam the refactor had to keep: result() is the one place that
// assembles builtAgent.
func TestBuildAgentResultIsComplete(t *testing.T) {
	d, sess := buildTestDeps(t)
	b, err := buildAgent(d, sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	for name, got := range map[string]any{
		"agent": b.agent, "task": b.task, "diag": b.diag, "repo": b.repo,
		"archive": b.archive, "ws": b.ws, "terms": b.terms, "cd": b.cd,
		"ruleInjector": b.ruleInjector, "skillInjector": b.skillInjector,
	} {
		if got == nil {
			t.Errorf("builtAgent.%s is nil", name)
		}
	}
	if b.skills == nil || b.rules == nil {
		t.Error("builtAgent lost the skill or rule lookups")
	}
	if b.agent.Root != d.cwd {
		t.Errorf("agent root = %q, want %q", b.agent.Root, d.cwd)
	}
	if _, err := os.Stat(d.cwd); err != nil {
		t.Fatal(err)
	}
}
