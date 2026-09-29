package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// --recipe makes no model call, so it runs with no API key at all; an
// unknown name is a usage error that says what is saved.
func TestRecipeRunsWithoutAKey(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Chdir(work)
	os.WriteFile(filepath.Join(work, "f.txt"), []byte("hello\n"), 0o644)
	os.MkdirAll(filepath.Join(work, ".janetcode", "recipes"), 0o755)
	os.WriteFile(filepath.Join(work, ".janetcode", "recipes", "look.json"), []byte(`{"name":"look","steps":[{"tool":"read_file","input":{"path":"f.txt"}}]}`), 0o644)

	stdout, _ := captureStd(t)
	// An untrusted project's recipe does not run: it is the project's
	// own list of tool calls.
	if err := run([]string{"--recipe", "look"}); headless.ExitCode(err) != headless.ExitBadInput || !strings.Contains(err.Error(), "--trust full") {
		t.Fatalf("an untrusted recipe ran: %v", err)
	}
	if err := run([]string{"--recipe", "look", "--trust", "full"}); err != nil {
		t.Fatalf("a recipe with no key: %v", err)
	}
	if out := stdout(); !strings.Contains(out, "hello") {
		t.Fatalf("the step did not run:\n%s", out)
	}

	err := run([]string{"--recipe", "nope"})
	if headless.ExitCode(err) != headless.ExitBadInput || !strings.Contains(err.Error(), "saved: look") {
		t.Fatalf("unknown recipe: %v (exit %d)", err, headless.ExitCode(err))
	}
}

// A second --init or --init-project is said once, as a usage error, with
// a next step a shell can take rather than a session's /init force.
func TestInitTwiceSaysWhatToDoNext(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Chdir(work)
	os.WriteFile(filepath.Join(work, "only.txt"), []byte("x\n"), 0o644)
	stdout, _ := captureStd(t)
	for _, flag := range []string{"--init", "--init-project"} {
		if err := run([]string{flag}); err != nil {
			t.Fatalf("first %s: %v", flag, err)
		}
		err := run([]string{flag})
		if headless.ExitCode(err) != headless.ExitBadInput || !strings.Contains(err.Error(), "already exists and was not touched") || strings.Contains(err.Error(), "/init force") {
			t.Fatalf("second %s: %v", flag, err)
		}
	}
	if out := stdout(); strings.Contains(out, "already exists") || strings.Contains(out, "1 files") {
		t.Fatalf("stdout:\n%s", out)
	}
}

// --sessions counts messages in words ("1 msg", not "1   msgs") and
// clips a long title with an ellipsis instead of running past the line.
func TestSessionsListReadsCleanly(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Chdir(work)
	cwd, _ := os.Getwd()
	dir, _ := session.Dir()
	s, err := session.New(dir, cwd, "openai", "m")
	if err != nil {
		t.Fatal(err)
	}
	m := llm.TextMessage(llm.RoleUser, "hi")
	if _, err := s.Append(session.Event{Type: session.TypeUserMessage, Surface: true, Message: &m}); err != nil {
		t.Fatal(err)
	}
	s.SetTitle(strings.Repeat("a very long title ", 10))
	stdout, _ := captureStd(t)
	if err := printSessions(); err != nil {
		t.Fatal(err)
	}
	out := stdout()
	if !strings.Contains(out, " 1 msg ") || strings.Contains(out, "msgs") || !strings.Contains(out, "…") {
		t.Fatalf("sessions:\n%s", out)
	}
}

// A session subject asked for with no key is a usage error, not a report
// that exits 0.
func TestInspectSessionWithoutAKeyIsAUsageError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Chdir(t.TempDir())
	stdout, _ := captureStd(t)
	err := run([]string{"--inspect", "session"})
	out := stdout()
	if headless.ExitCode(err) != headless.ExitBadInput || !strings.Contains(err.Error(), "describes a running session") || out != "" {
		t.Fatalf("err = %v (exit %d), stdout %q", err, headless.ExitCode(err), out)
	}
	if err := run([]string{"--inspect", "config"}); err != nil {
		t.Fatalf("a subject that needs no session: %v", err)
	}
}
