package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/trust"
)

func writeProjectSkill(t *testing.T, cwd string) {
	t.Helper()
	dir := filepath.Join(cwd, ".janetcode", "skills", "deploy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: deploy\ndescription: ship it\n---\nrun the deploy"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillNames(b builtAgent) []string {
	var out []string
	for _, s := range b.skills() {
		out = append(out, s.Name)
	}
	return out
}

// A project's own skills stay out until the project is trusted, and come
// in the moment it is, without a restart.
func TestProjectSkillsFollowTheTrustLevel(t *testing.T) {
	d, sess := buildTestDeps(t)
	writeProjectSkill(t, d.cwd)
	d.gate = &trust.Gate{Store: trust.Store{Path: filepath.Join(d.cfgDir, "trust.json")}}
	b, err := buildAgent(d, sess, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.close)
	if names := strings.Join(skillNames(b), ","); strings.Contains(names, "deploy") {
		t.Fatalf("an undecided project's skill was loaded: %s", names)
	}
	if err := d.gate.Set(d.cwd, trust.Instructions, false); err != nil {
		t.Fatal(err)
	}
	if names := strings.Join(skillNames(b), ","); !strings.Contains(names, "deploy") {
		t.Fatalf("a trusted project's skill is missing: %s", names)
	}
	if line := trustNotice(d.gate, d.cwd); line != "" {
		t.Errorf("a decided project still gets the notice: %q", line)
	}
}

func TestTrustNoticeNamesWhatIsHeldBack(t *testing.T) {
	cwd := t.TempDir()
	writeProjectSkill(t, cwd)
	g := &trust.Gate{Store: trust.Store{Path: filepath.Join(t.TempDir(), "trust.json")}}
	line := trustNotice(g, cwd)
	if !strings.Contains(line, "skills") || !strings.Contains(line, "--trust") {
		t.Fatalf("notice = %q", line)
	}
	if trustNotice(g, t.TempDir()) != "" {
		t.Error("a project with nothing to trust got the notice")
	}
}

func TestTrustFlagParses(t *testing.T) {
	g, err := newTrustGate(t.TempDir(), "instructions", func(string) string { return "" })
	if err != nil || g.Flag != trust.Instructions {
		t.Fatalf("--trust instructions = %v, %v", g, err)
	}
	g, err = newTrustGate(t.TempDir(), "", func(k string) string {
		if k == "JANETCODE_TRUST" {
			return "full"
		}
		return ""
	})
	if err != nil || g.Flag != trust.Full {
		t.Fatalf("JANETCODE_TRUST=full = %v, %v", g, err)
	}
	if _, err := newTrustGate(t.TempDir(), "sorta", func(string) string { return "" }); headless.ExitCode(err) != headless.ExitBadInput {
		t.Fatalf("bad level: %v", err)
	}
}
