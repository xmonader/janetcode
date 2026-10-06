package tui

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestEditorCommand(t *testing.T) {
	env := map[string]string{"EDITOR": "code --wait"}
	if got := editorCommand(func(k string) string { return env[k] }); !reflect.DeepEqual(got, []string{"code", "--wait"}) {
		t.Fatalf("EDITOR: %v", got)
	}
	env["VISUAL"] = "nvim"
	if got := editorCommand(func(k string) string { return env[k] }); !reflect.DeepEqual(got, []string{"nvim"}) {
		t.Fatalf("VISUAL wins: %v", got)
	}
	if got := editorCommand(func(string) string { return "  " }); !reflect.DeepEqual(got, []string{"vi"}) {
		t.Fatalf("fallback: %v", got)
	}
}

// What the editor saved becomes the prompt; an editor that exits non-zero
// leaves the prompt alone.
func TestFinishEditor(t *testing.T) {
	here := t.TempDir()
	sess := newSession(t, here)
	m := newModel(Options{Agent: &agent.Agent{Tools: tool.NewRegistry(), Model: "m", Log: sess, Root: here}, Session: sess, Provider: "fake", Cwd: here, Version: "test"})
	m.input.SetValue("draft")
	m.finishEditor(editorDoneMsg{text: "line one\n\nline two\n"})
	if m.input.Value() != "line one\n\nline two" {
		t.Fatalf("value = %q", m.input.Value())
	}
	err := exec.Command("sh", "-c", "exit 1").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatal("setup")
	}
	m.finishEditor(editorDoneMsg{err: err})
	if m.input.Value() != "line one\n\nline two" {
		t.Fatalf("cancelled edit changed the prompt: %q", m.input.Value())
	}
}
