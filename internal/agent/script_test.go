package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func pythonOrSkip(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	return p
}

func scriptAgent(t *testing.T, root string, extra ...tool.Tool) (*Agent, *Script) {
	t.Helper()
	tools := append([]tool.Tool{
		tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, tool.EditFile{Root: root},
		tool.ListDir{Root: root}, tool.Grep{Root: root},
	}, extra...)
	ag := &Agent{Tools: tool.NewRegistry(tools...), Log: &session.Memory{}, AutoApprove: true}
	s := &Script{Parent: ag, Root: root}
	ag.Tools.Add(s)
	return ag, s
}

func runScript(t *testing.T, s *Script, code string) string {
	t.Helper()
	in, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Run(context.Background(), in)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out
}

// TestScriptCallsTools is the whole point: several tool calls, the logic
// between them, and one result.
func TestScriptCallsTools(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	ag, s := scriptAgent(t, root)
	for _, f := range []struct{ name, body string }{
		{"a.txt", "alpha\nneedle\n"},
		{"b.txt", "beta\n"},
		{"c.txt", "needle\ngamma\n"},
	} {
		if _, err := (tool.WriteFile{Root: root}).Run(context.Background(),
			json.RawMessage(`{"path":"`+f.name+`","content":`+quote(f.body)+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	out := runScript(t, s, `
from janetcode_tools import read_file
hits = [n for n in ("a.txt", "b.txt", "c.txt") if "needle" in read_file(path=n)]
print(",".join(hits))
`)
	if !strings.Contains(out, "a.txt,c.txt") {
		t.Fatalf("got %q", out)
	}
	// The turn's cost is reported, and every nested call is on the log.
	if !strings.Contains(out, "[3 tool calls]") {
		t.Fatalf("call count missing: %q", out)
	}
	n := 0
	for _, e := range ag.Log.Events() {
		if e.Type == session.TypeToolCall && e.Name == "read_file" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("log recorded %d nested calls, want 3", n)
	}
}

// TestScriptToolError gives the script the failure rather than swallowing it.
func TestScriptToolError(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	_, s := scriptAgent(t, root)
	out := runScript(t, s, `
from janetcode_tools import read_file, ToolError
try:
    read_file(path="nope.txt")
    print("NO ERROR")
except ToolError as e:
    print("caught:", "nope.txt" in str(e))
`)
	if !strings.Contains(out, "caught: True") {
		t.Fatalf("got %q", out)
	}
}

// TestScriptRespectsReadOnly: a script must not be a way around plan mode.
func TestScriptRespectsReadOnly(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	ag, s := scriptAgent(t, root)
	ag.ReadOnly = true
	out := runScript(t, s, `
from janetcode_tools import write_file, ToolError
try:
    write_file(path="x.txt", content="hi")
    print("WROTE")
except ToolError as e:
    print("refused:", "read-only" in str(e))
`)
	if !strings.Contains(out, "refused: True") {
		t.Fatalf("got %q", out)
	}
}

// TestScriptCallLimit stops a runaway loop instead of letting it run for
// the whole timeout.
func TestScriptCallLimit(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	_, s := scriptAgent(t, root)
	out := runScript(t, s, `
from janetcode_tools import list_dir, ToolError
n = 0
try:
    while True:
        list_dir(path=".")
        n += 1
except ToolError as e:
    print("stopped after", n, "limit" in str(e) or "tool calls" in str(e))
`)
	if !strings.Contains(out, "stopped after 50 True") {
		t.Fatalf("got %q", out)
	}
}

// TestScriptFailureIsReported: a script that crashes says so, with the
// traceback, instead of coming back as an empty success.
func TestScriptFailureIsReported(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	_, s := scriptAgent(t, root)
	out := runScript(t, s, "print('before')\nraise SystemExit(3)\n")
	if !strings.Contains(out, "before") || !strings.Contains(out, "the script failed") {
		t.Fatalf("got %q", out)
	}
}

// A script that starts something in the background and exits left the
// tool waiting on the output pipe the background process still held: the
// turn hung until that process ended, or for the whole five-minute limit
// and then reported a script that had finished as killed.
func TestAScriptsBackgroundChildDoesNotHoldTheTool(t *testing.T) {
	pythonOrSkip(t)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep command")
	}
	root := t.TempDir()
	_, s := scriptAgent(t, root)
	start := time.Now()
	out := runScript(t, s, "import subprocess\nsubprocess.Popen(['sleep', '30'])\nprint('started')\n")
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("the tool waited %s on the script's background child", took)
	}
	if !strings.Contains(out, "started") || strings.Contains(out, "failed") || strings.Contains(out, "killed") {
		t.Fatalf("got %q, want the script's output reported as a success", out)
	}
}

// TestScriptDoesNotSeeTheAgentsKey: the script is code the model wrote,
// run on the host. It gets the environment every other model-run command
// gets — without the provider key — or one line of urllib sends the key
// wherever the model was told to, and redacting the printed output does
// nothing about that.
func TestScriptDoesNotSeeTheAgentsKey(t *testing.T) {
	pythonOrSkip(t)
	t.Setenv("DEEPSEEK_API_KEY", "plain-value-the-redactor-would-not-catch")
	root := t.TempDir()
	_, s := scriptAgent(t, root)
	out := runScript(t, s, "import os\nprint('key=' + repr(os.environ.get('DEEPSEEK_API_KEY')))\n")
	if !strings.Contains(out, "key=None") {
		t.Fatalf("the script saw the provider key: %q", out)
	}
}

// TestScriptDescriptionListsTools keeps the description in step with what
// the session actually has, and must not recurse through the registry.
func TestScriptDescriptionListsTools(t *testing.T) {
	root := t.TempDir()
	ag, s := scriptAgent(t, root)
	d := s.Description()
	for _, want := range []string{"read_file", "edit_file", "grep"} {
		if !strings.Contains(d, want) {
			t.Fatalf("description omits %s: %s", want, d)
		}
	}
	if strings.Contains(d, "execute_code,") {
		t.Fatal("a script must not offer to run a script")
	}
	// Building the definitions calls Description under the registry lock.
	if len(ag.Tools.Defs()) == 0 {
		t.Fatal("no definitions")
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestScriptEditIsApproved is the safety property: an edit made from
// inside a script goes to the user with its diff, exactly like one the
// model made itself, and a refusal reaches the script as an error.
func TestScriptEditIsApproved(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	ag, s := scriptAgent(t, root)
	ag.AutoApprove = false
	if _, err := (tool.WriteFile{Root: root}).Run(context.Background(),
		json.RawMessage(`{"path":"f.txt","content":"one\ntwo\n"}`)); err != nil {
		t.Fatal(err)
	}
	var asked []ToolCall
	ag.Approver = ApproverFunc(func(_ context.Context, c ToolCall) (Decision, error) {
		asked = append(asked, c)
		if strings.Contains(string(c.Input), "two") {
			return Deny, nil
		}
		return Allow, nil
	})
	out := runScript(t, s, `
from janetcode_tools import edit_file, ToolError
print(edit_file(path="f.txt", old_string="one", new_string="ONE").splitlines()[0])
try:
    edit_file(path="f.txt", old_string="two", new_string="TWO")
    print("NOT REFUSED")
except ToolError:
    print("refused")
`)
	if !strings.Contains(out, "Edited f.txt") || !strings.Contains(out, "refused") {
		t.Fatalf("got %q", out)
	}
	if len(asked) != 2 {
		t.Fatalf("the user was asked %d times, want 2", len(asked))
	}
	if !strings.Contains(asked[0].Preview, "-one") || !strings.Contains(asked[0].Preview, "+ONE") {
		t.Fatalf("no diff in the prompt: %q", asked[0].Preview)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "ONE\ntwo\n" {
		t.Fatalf("file is %q; the denied edit must not have landed", got)
	}
}

// TestScriptPlainStripsGutter: read_file numbers its lines, so a script
// matching on the text itself has to strip that first. Getting this wrong
// produces a confident wrong answer rather than an error, which is why the
// helper exists and is named in the tool's description.
func TestScriptPlainStripsGutter(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	_, s := scriptAgent(t, root)
	if _, err := (tool.WriteFile{Root: root}).Run(context.Background(),
		json.RawMessage(`{"path":"a.go","content":"package x\nfunc A() {}\nfunc B() {}\n"}`)); err != nil {
		t.Fatal(err)
	}
	out := runScript(t, s, `
from janetcode_tools import read_file, plain
body = read_file(path="a.go")
raw = sum(1 for l in body.split("\n") if l.startswith("func "))
clean = sum(1 for l in plain(body).split("\n") if l.startswith("func "))
print(raw, clean)
`)
	if !strings.Contains(out, "0 2") {
		t.Fatalf("got %q", out)
	}
	if !strings.Contains(s.Description(), "plain(") {
		t.Fatal("the description must point at plain()")
	}
}

// TestScriptArchivesNestedOutput: what a script read never enters the
// conversation, but it must still be findable afterwards.
func TestScriptArchivesNestedOutput(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	ag, s := scriptAgent(t, root)
	ag.Archive = NewArchive(t.TempDir())
	big := strings.Repeat("a long line of file content that will not fit inline\n", 800)
	if _, err := (tool.WriteFile{Root: root}).Run(context.Background(),
		json.RawMessage(`{"path":"big.txt","content":`+quote(big)+`}`)); err != nil {
		t.Fatal(err)
	}
	out := runScript(t, s, `
from janetcode_tools import read_file
print(len(read_file(path="big.txt")))
`)
	if strings.Contains(out, "[the script failed") {
		t.Fatalf("script failed: %q", out)
	}
	saved := 0
	for _, e := range ag.Log.Events() {
		if e.Type == session.TypeSpillSave {
			saved++
		}
	}
	if saved == 0 {
		t.Fatal("a script's big read should still reach the archive")
	}
}

// TestRenderScripts is how a user finds out what a script did on their
// machine: the code, the calls, and what came back.
func TestRenderScripts(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	ag, _ := scriptAgent(t, root)
	if got := RenderScripts(ag.Log.Events()); !strings.Contains(got, "no script") {
		t.Fatalf("got %q", got)
	}
	// The script tool is run through the agent so the outer call is logged
	// too, as it would be in a session.
	in, _ := json.Marshal(map[string]string{"code": "from janetcode_tools import list_dir\nlist_dir(path='.')\nprint('done')\n"})
	out, isErr, _ := ag.runNested(context.Background(), "outer", "execute_code", in)
	if isErr {
		t.Fatalf("script failed: %s", out)
	}
	report := RenderScripts(ag.Log.Events())
	for _, want := range []string{"script 1 (finished, 1 tool call)", "| print('done')", "→ list_dir", "printed:", "done"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report is missing %q:\n%s", want, report)
		}
	}
}

// TestScriptPositionalArgs is the regression test for the defect a
// measured run found: stubs that took keyword arguments only made a
// quarter of the scripts in that run die on read_file("path") with a
// TypeError, and each death cost a whole extra turn.
func TestScriptPositionalArgs(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	_, s := scriptAgent(t, root)
	if _, err := (tool.WriteFile{Root: root}).Run(context.Background(),
		json.RawMessage(`{"path":"a.txt","content":"hello\n"}`)); err != nil {
		t.Fatal(err)
	}
	out := runScript(t, s, `
from janetcode_tools import read_file, list_dir
print("positional:", "hello" in read_file("a.txt"))
print("keyword:", "hello" in read_file(path="a.txt"))
print("listing:", "a.txt" in list_dir("."))
`)
	for _, want := range []string{"positional: True", "keyword: True", "listing: True"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	// The description shows the signature, so the model does not have to
	// guess which argument comes first.
	if d := s.Description(); !strings.Contains(d, "read_file(path, ...)") {
		t.Fatalf("description should show the first argument: %s", d)
	}
}

// TestScriptStripsIntent: a script sees the same tool schemas the model
// does, so it passes intent= as a keyword. A real model did exactly that
// and every call in the script failed on an unknown field.
func TestScriptStripsIntent(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	ag, s := scriptAgent(t, root)
	ag.Tools.Intent = true
	if _, err := (tool.WriteFile{Root: root}).Run(context.Background(),
		json.RawMessage(`{"path":"f.txt","content":"one\ntwo\n"}`)); err != nil {
		t.Fatal(err)
	}
	out := runScript(t, s, `
from janetcode_tools import edit_file
print(edit_file(path="f.txt", intent="rename the first line", old_string="one", new_string="ONE").splitlines()[0])
`)
	if !strings.Contains(out, "Edited f.txt") {
		t.Fatalf("an intent keyword must not fail the call: %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.txt"))
	if string(got) != "ONE\ntwo\n" {
		t.Fatalf("file is %q", got)
	}
	// And it is recorded, like any other call's reason.
	var seen string
	for _, e := range ag.Log.Events() {
		if e.Type == session.TypeToolCall && e.Intent != "" {
			seen = e.Intent
		}
	}
	if seen != "rename the first line" {
		t.Fatalf("the intent should be logged, got %q", seen)
	}
}
