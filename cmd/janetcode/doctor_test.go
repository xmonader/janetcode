package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
)

// TestDoctorReportsProjectFeatures runs doctor in a project with hooks,
// verification commands, permission rules and a skill but no credentials:
// the credential check fails, and every project row still reports.
func TestDoctorReportsProjectFeatures(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	os.MkdirAll(filepath.Join(work, ".janetcode", "skills", "review"), 0o755)
	files := map[string]string{
		".janetcode/hooks.json":             `{"pre_tool": [{"command": "echo hi"}], "stop": [{"command": "true"}]}`,
		".janetcode/verify.json":            `{"commands": ["go build ./..."]}`,
		".janetcode/permissions.json":       `{"allow": ["bash(go test *)"], "deny": ["bash(rm -rf *)"]}`,
		".janetcode/skills/review/SKILL.md": "---\ndescription: review code\n---\nlook carefully\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := os.Getwd()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })

	var out bytes.Buffer
	err := doctor(&out, config.Overrides{})
	if err == nil || !strings.Contains(err.Error(), "check(s) failed") {
		t.Fatalf("err = %v; want a credential failure", err)
	}
	got := out.String()
	for _, want := range []string{
		"[FAIL] credentials",
		"[ok  ] hooks          2 hooks in .janetcode/hooks.json · not trusted yet (/hooks trust in the TUI) · 0 global",
		"[ok  ] verify         1 command in .janetcode/verify.json · not trusted yet (/verify trust in the TUI)",
		"[ok  ] permissions    1 allow / 1 deny in .janetcode/permissions.json · allow rules not trusted yet",
		"[ok  ] skills         1 skill (1 project, 0 global, 0 claude)",
		"[ok  ] memory         0 memory entries (0 project, 0 global)",
		"lexical recall only",
		"lsp gopls",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}

	// A broken hooks file is a failure, not a crash.
	os.WriteFile(filepath.Join(work, ".janetcode", "hooks.json"), []byte(`{"bogus_event": []}`), 0o644)
	out.Reset()
	doctor(&out, config.Overrides{})
	if !strings.Contains(out.String(), "[FAIL] hooks") {
		t.Fatalf("broken hooks not reported:\n%s", out.String())
	}
}
