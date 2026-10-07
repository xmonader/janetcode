package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// A script that imports a tool under a name the model guessed — wrong case,
// no underscore — is told the real name instead of dying on an import.
func TestScriptMisspeltToolNamesTheRealOne(t *testing.T) {
	pythonOrSkip(t)
	_, s := scriptAgent(t, t.TempDir())
	out := runScript(t, s, `
from janetcode_tools import readfile, ToolError
import janetcode_tools
for f in (readfile, janetcode_tools.Grep):
    try:
        f(path="x")
    except ToolError as e:
        print(e)
`)
	for _, want := range []string{"no tool 'readfile'; did you mean read_file()?", "no tool 'Grep'; did you mean grep()?"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// call() with a name no tool has goes to the session, which answers with
// the closest tool on the same line.
func TestUnknownToolNamesTheClosest(t *testing.T) {
	ag := &Agent{Tools: tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}), Log: &session.Memory{}, AutoApprove: true}
	out, isErr, _ := ag.runNested(context.Background(), "x", "Read_File", json.RawMessage(`{}`))
	if !isErr || !strings.Contains(out, `did you mean "read_file"?`) {
		t.Fatalf("isErr=%v out=%q", isErr, out)
	}
	out, _, _ = ag.runNested(context.Background(), "y", "frobnicate", json.RawMessage(`{}`))
	if strings.Contains(out, "did you mean") {
		t.Fatalf("suggested something for a name nothing resembles: %q", out)
	}
}

// A keyword the tool does not take names the one it probably meant; a
// value of the wrong kind says the shape in JSON's words, not Go's.
func TestToolArgumentErrorsSayWhatIsExpected(t *testing.T) {
	ag := &Agent{Tools: tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}), Log: &session.Memory{}, AutoApprove: true}
	out, _, _ := ag.runNested(context.Background(), "a", "read_file", json.RawMessage(`{"path":"x","limt":5}`))
	if !strings.Contains(out, `did you mean "limit"?`) {
		t.Fatalf("unknown field: %q", out)
	}
	out, _, _ = ag.runNested(context.Background(), "b", "read_file", json.RawMessage(`{"path":"x","limit":"ten"}`))
	if !strings.Contains(out, "limit must be a whole number, not a string") || strings.Contains(out, "Go struct") {
		t.Fatalf("type error: %q", out)
	}
}

// A script that computed its answer and never printed it is told why
// nothing came back.
func TestScriptThatPrintsNothingSaysWhy(t *testing.T) {
	pythonOrSkip(t)
	_, s := scriptAgent(t, t.TempDir())
	out := runScript(t, s, "x = 1 + 1\n")
	if !strings.Contains(out, "print() the results you need") {
		t.Fatalf("got %q", out)
	}
}
