package prompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemIncludesCwdAndInstructions(t *testing.T) {
	dir := t.TempDir()
	s := System(dir)
	if !strings.Contains(s, "Working directory: "+dir) || strings.Contains(s, "Project instructions") {
		t.Fatalf("prompt = %q", s)
	}
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("use tabs"), 0o644)
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("use spaces"), 0o644)
	s = System(dir)
	if !strings.Contains(s, "(from AGENTS.md)\nuse spaces") || strings.Contains(s, "use tabs") {
		t.Fatalf("precedence wrong: %q", s)
	}
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	if s = System(dir); !strings.Contains(s, "branch main") {
		t.Fatalf("git info missing: %q", s)
	}
}

func TestSystemMDReplacesTheBaseButKeepsTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "sys.md")
	if err := os.WriteFile(file, []byte("You are a very small agent.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == SystemMDEnv {
			return file
		}
		return ""
	}
	ov, err := LoadOverride(env, "")
	if err != nil {
		t.Fatal(err)
	}
	got := SystemWith(dir, ov)
	if !strings.HasPrefix(got, "You are a very small agent.") {
		t.Fatalf("prompt = %q", got)
	}
	if strings.Contains(got, "You are janetcode") {
		t.Fatal("the base prompt was not replaced")
	}
	if !strings.Contains(got, "Working directory: "+dir) || !strings.Contains(got, "Platform:") {
		t.Fatalf("the environment section is missing: %q", got)
	}
}

func TestAppendSystemPromptFromTextAndFile(t *testing.T) {
	dir := t.TempDir()
	none := func(string) string { return "" }
	ov, err := LoadOverride(none, "  Answer in French.  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := SystemWith(dir, ov); !strings.Contains(got, "You are janetcode") || !strings.HasSuffix(strings.TrimSpace(got), "Answer in French.") {
		t.Fatalf("prompt = %q", got)
	}
	file := filepath.Join(dir, "extra.md")
	if err := os.WriteFile(file, []byte("Prefer tables.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ov, err = LoadOverride(none, "@"+file)
	if err != nil {
		t.Fatal(err)
	}
	if got := SystemWith(dir, ov); !strings.Contains(got, "Prefer tables.") {
		t.Fatalf("prompt = %q", got)
	}
}

func TestLoadOverrideRefusesAMissingOrEmptyFile(t *testing.T) {
	missing := func(k string) string {
		if k == SystemMDEnv {
			return filepath.Join(t.TempDir(), "nope.md")
		}
		return ""
	}
	if _, err := LoadOverride(missing, ""); err == nil {
		t.Fatal("a missing prompt file must be an error, not a silent default")
	}
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.md")
	os.WriteFile(empty, []byte("  \n"), 0o644)
	if _, err := LoadOverride(func(k string) string {
		if k == SystemMDEnv {
			return empty
		}
		return ""
	}, ""); err == nil {
		t.Fatal("an empty prompt file must be an error")
	}
	if _, err := LoadOverride(func(string) string { return "" }, "@/no/such/file"); err == nil {
		t.Fatal("a missing append file must be an error")
	}
}
