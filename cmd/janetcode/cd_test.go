package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/plan"
	"github.com/xmonader/janetcode/internal/session"
)

// cdBuild wires a real session the way the composition root does, in one
// of two sibling directories, and hands back the pieces a /cd has to move.
func cdBuild(t *testing.T) (b builtAgent, here, sib string) {
	t.Helper()
	base := t.TempDir()
	here, sib = filepath.Join(base, "here"), filepath.Join(base, "sibling")
	for _, d := range []string{here, sib} {
		if err := os.MkdirAll(filepath.Join(d, ".janetcode"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sessDir := t.TempDir()
	sess, err := session.New(sessDir, here, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	b, err = buildAgent(agentDeps{
		prov:    &fakeResumeProvider{},
		cfg:     &config.Config{Model: "m"},
		cwd:     here,
		cfgDir:  t.TempDir(),
		sessDir: sessDir,
		mem:     memory.Open(t.TempDir(), here),
	}, sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	return b, here, sib
}

// TestNothingStillPointsAtTheOldRoot walks every consumer of the project
// root that buildAgent registers and checks it actually moved. The table
// is the point: a new thing rooted at the working directory is registered
// with the tracker, and this test then holds it to the move.
func TestNothingStillPointsAtTheOldRoot(t *testing.T) {
	b, here, sib := cdBuild(t)
	before := b.cd.Consumers()
	if len(before) == 0 {
		t.Fatal("no consumers of the project root were registered")
	}
	for _, want := range []string{
		"system prompt", "repository map", "memory", "skills and rules",
		"subagents", "language servers", "snapshots", "running commands",
	} {
		if !contains(before, want) {
			t.Errorf("%q is rooted at the project but is not registered with /cd", want)
		}
	}

	if _, err := b.cd.Change(sib); err != nil {
		t.Fatal(err)
	}

	ag := b.agent
	for _, c := range []struct {
		what string
		got  string
	}{
		{"cd tracker", b.cd.Dir()},
		{"workspace (file tools and bash)", b.ws.Root()},
		{"agent root", ag.RootDir()},
		// plan.File is <root>/.janetcode/plan.md, so two Dirs is the root.
		{"plan file", filepath.Dir(filepath.Dir(plan.File(ag.RootDir())))},
		{"repository map", b.repo.Root},
		{"language servers", b.diag.Root},
		{"subagents", b.task.Root},
	} {
		if c.got == here {
			t.Errorf("%s still points at %s", c.what, here)
		}
		if c.got != sib {
			t.Errorf("%s = %q, want %q", c.what, c.got, sib)
		}
	}
	// The system prompt's environment section is what tells the model
	// where it is, and it is built once — so it has to be rebuilt.
	sys := ag.SystemPrompt()
	if !strings.Contains(sys, "Working directory: "+sib) {
		t.Errorf("the system prompt does not state the new working directory:\n%s", tailOf(sys))
	}
	if strings.Contains(sys, "Working directory: "+here) {
		t.Error("the system prompt still states the directory the session left")
	}
	// The cached part of the prompt is everything above "# Environment";
	// a move must not touch a byte of it.
	head, _, _ := strings.Cut(sys, "\n# Environment\n")
	if !strings.Contains(head, "You are janetcode") || strings.Contains(head, here) || strings.Contains(head, sib) {
		t.Error("the working directory leaked into the cached part of the system prompt")
	}
}

func TestCdRegistersEveryProjectFile(t *testing.T) {
	b, here, sib := cdBuild(t)
	r := &root{
		opts: &options{}, cfg: &config.Config{Model: "m"}, ag: b.agent,
		cfgDir: t.TempDir(), cwd: here, built: b,
	}
	r.registerProjectRooted(b.cd)
	for _, want := range []string{"project hooks", "verification", "permissions", "mcp servers", "composition root"} {
		if !contains(b.cd.Consumers(), want) {
			t.Errorf("%q is rooted at the project but is not registered with /cd", want)
		}
	}
	// A verify config in the directory we move to is discovered there, and
	// stays untrusted: approval does not travel with the session.
	if err := os.WriteFile(filepath.Join(sib, ".janetcode", "verify.json"),
		[]byte(`{"commands":["go test ./..."]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := b.cd.Change(sib)
	if err != nil {
		t.Fatal(err)
	}
	if r.cwd != sib {
		t.Errorf("the composition root still says %q", r.cwd)
	}
	if !r.verifyDefined {
		t.Error("the new directory's verify.json was not discovered")
	}
	if r.verifyTrusted {
		t.Error("an untrusted verify.json in the new directory was trusted")
	}
	gate, untrusted := b.agent.VerifyGate()
	if gate != nil || !untrusted {
		t.Errorf("verify gate = %v, untrusted = %v; want no gate and a warning", gate, untrusted)
	}
	if !strings.Contains(status, "not trusted yet") {
		t.Errorf("the move did not say the new directory's checks are untrusted:\n%s", status)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func tailOf(s string) string {
	if i := strings.Index(s, "\n# Environment\n"); i >= 0 {
		return s[i:]
	}
	return s
}
