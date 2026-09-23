package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// The nine behaviours that landed without a switch. Each Off name must
// be in Switches (so --eval-off can name it) and must restore the
// pre-feature behaviour rather than a degraded one.
var newSwitches = []string{
	"accept", "recipe", "proof", "impact", "routing",
	"protected", "ladder", "undoabout", "mermaid", "keeprevert",
}

func TestNewSwitchesAreKnown(t *testing.T) {
	for _, name := range newSwitches {
		if !KnownSwitch(name) {
			t.Errorf("%q is not in Switches(): %s", name, strings.Join(Switches(), ", "))
		}
	}
	off, err := ParseSwitches(strings.Join(newSwitches, ","))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range newSwitches {
		if !off[name] {
			t.Errorf("ParseSwitches dropped %q", name)
		}
	}
}

func TestOffAcceptRefusesTheCommandAndIgnoresALock(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "edit_file", `{"path":"foo_test.go","old_string":"package foo","new_string":"package foo // ok"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Off = map[string]bool{"accept": true}
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.EditFile{Root: root})
	testPath := filepath.Join(root, "foo_test.go")
	if err := os.WriteFile(testPath, []byte("package foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AcceptCommand(AcceptArgs{Request: "add foo"}, root); err == nil {
		t.Fatal("AcceptCommand succeeded with accept off")
	}
	sum := shaOf(t, testPath)
	a.SetAccept(&session.AcceptMeta{
		Phase: session.AcceptLocked, Request: "add foo", Root: root,
		Files: []string{testPath}, Hashes: map[string]string{testPath: sum}, Command: "go test ./...",
	}, nil)
	if err := a.Run(context.Background(), "tweak the test", nil); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(testPath); !strings.Contains(string(data), "ok") {
		t.Fatalf("accept off must not enforce a lock: %q", data)
	}
}

func TestOffRecipeRefusesSaveAndRun(t *testing.T) {
	a, root := newAgent(t, &fake{}, nil)
	a.Off = map[string]bool{"recipe": true}
	if _, err := a.RecipeSave("", root, "demo"); err == nil {
		t.Fatal("RecipeSave succeeded with recipe off")
	}
	if _, _, err := a.RunRecipe(context.Background(), recipe.Recipe{Name: "demo"}, nil, nil); err == nil {
		t.Fatal("RunRecipe succeeded with recipe off")
	}
}

func TestOffProofDoesNotAttach(t *testing.T) {
	a, _ := newAgent(t, &fake{}, nil)
	a.Off = map[string]bool{"proof": true}
	got, err := a.AttachProof(context.Background(), t.TempDir(), "deadbeef")
	if err != nil {
		t.Fatalf("AttachProof with proof off: %v", err)
	}
	if got != "" {
		t.Fatalf("AttachProof attached %q with proof off", got)
	}
}

func TestOffImpactSkipsTheReachLine(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var seen ToolCall
	a := &Agent{
		Provider: &fake{responses: []*llm.Response{
			toolCall("e1", "edit_file", `{"path":"a.go","old_string":"package a","new_string":"package a // x"}`),
			text("done"),
		}},
		Tools: tool.NewRegistry(tool.EditFile{Root: root}),
		Model: "m",
		Off:   map[string]bool{"impact": true},
	}
	a.Approver = ApproverFunc(func(_ context.Context, call ToolCall) (Decision, error) { seen = call; return Allow, nil })
	a.Impact = func(context.Context, ToolCall) string {
		calls++
		return "changes Foo (func)"
	}
	if err := a.Run(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("Impact called %d times with impact off", calls)
	}
	if seen.Impact != "" {
		t.Fatalf("prompt carried impact %q with impact off", seen.Impact)
	}
}

func TestOffRoutingNeverLeavesModel(t *testing.T) {
	a := routableAgent(true)
	a.Off = map[string]bool{"routing": true}
	if _, _, _, ok := a.routeStep(func(Event) {}); ok {
		t.Fatal("routeStep routed with routing off")
	}
}

func TestOffProtectedDoesNotForceAPrompt(t *testing.T) {
	var seen []ToolCall
	a, root := protectedAgent(t, &seen)
	a.AutoEdits = true
	a.Off = map[string]bool{"protected": true}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("protected off still asked: %+v", seen)
	}
	if _, err := os.Stat(filepath.Join(root, ".npmrc")); err != nil {
		t.Fatal("protected off must let the write through under /edits")
	}
}

func TestOffLadderKeepsTheSingleRule(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"docs/a.md","content":"1"}`),
		text("done"),
	}}
	ap := &rungApprover{pick: func(ToolCall) int { return 0 }}
	a, _ := newAgent(t, f, ap)
	a.Off = map[string]bool{"ladder": true}
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if len(ap.calls) != 1 {
		t.Fatalf("asked %d times", len(ap.calls))
	}
	call := ap.calls[0]
	if call.Rule != "write_file(docs/*)" && call.Rule != "write_file(docs/a.md)" {
		t.Fatalf("rule = %q, want Suggest's single rule", call.Rule)
	}
	if len(call.Rungs) != 1 || call.Rungs[0].Rule != call.Rule || call.RuleIndex != 0 {
		t.Fatalf("ladder off must keep one rung matching Rule: %+v", call)
	}
}

func TestOffUndoAboutSkipsTheModel(t *testing.T) {
	a, root, f := setupUndoIntentAgent(t)
	a.Off = map[string]bool{"undoabout": true}
	before := len(f.requests)
	sel, err := a.SelectChanges(context.Background(), "the logging change")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != before {
		t.Fatal("undoabout off still called the model")
	}
	if !sel.Fallback {
		t.Fatal("undoabout off must use the keyword fallback")
	}
	if len(sel.Undo) != 1 || sel.Undo[0].Path != filepath.Join(root, "logger.go") {
		t.Fatalf("keyword fallback should pick the logging change: undo = %+v", sel.Undo)
	}
}

func shaOf(t *testing.T, path string) string {
	t.Helper()
	sum, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}
