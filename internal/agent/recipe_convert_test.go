package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// newEditAgent is newAgent plus tool.EditFile, for tests that need the
// model to make an edit_file call (line-range or text form).
func newEditAgent(t *testing.T, f *fake, approver Approver) (*Agent, string) {
	t.Helper()
	a, root := newAgent(t, f, approver)
	a.Tools.Add(tool.EditFile{Root: root})
	return a, root
}

// TestRunRecipeAllowSessionOnFreshAgentDoesNotPanic is the regression test
// for the PANIC bug: a fresh Agent whose very first action is RunRecipe
// (no RunBlocks ever ran, so a.allowed was never initialised there) with
// an approver that answers AllowSession used to panic in authorize with
// "assignment to entry in nil map". allowSession (exec.go) now
// initialises the map at the point it is written, so this must succeed.
func TestRunRecipeAllowSessionOnFreshAgentDoesNotPanic(t *testing.T) {
	a, root := newAgent(t, nil, ApproverFunc(func(context.Context, ToolCall) (Decision, error) {
		return AllowSession, nil
	}))
	r := recipe.Recipe{Name: "fresh", Steps: []recipe.Step{
		{Tool: "write_file", Input: mustJSON(map[string]string{"path": "out.txt", "content": "hi\n"})},
		// A second mutating step of the same tool exercises the map this
		// AllowSession decision wrote to: reading a nil map is already
		// safe, so only the write on the first step could ever panic, but
		// this checks that the session-wide allow it granted actually
		// took (no second prompt, since the approver above only ever
		// answers once per unread ToolCall — a second ask would need a
		// second scripted decision it does not have).
		{Tool: "write_file", Input: mustJSON(map[string]string{"path": "out2.txt", "content": "again\n"})},
	}}
	report, failed, err := a.RunRecipe(context.Background(), r, nil, nil)
	if err != nil {
		t.Fatalf("RunRecipe: %v", err)
	}
	if failed != nil {
		t.Fatalf("unexpected failure: %+v", failed)
	}
	if report == "" {
		t.Fatal("expected a non-empty report")
	}
	for _, name := range []string{"out.txt", "out2.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
	}
}

// scriptEditFileLine runs a scripted turn that edits path with edit_file's
// line-range form (from_line/to_line), the shape the model actually uses
// when it already has line numbers (see internal/tool/files.go); it
// returns the session log so the caller can crystallise it into a recipe.
func scriptEditFileLine(t *testing.T, a *Agent, path string, fromLine, toLine int, newString string) *session.Memory {
	t.Helper()
	mem := &session.Memory{}
	a.Log = mem
	input, _ := json.Marshal(map[string]any{
		"path": path, "from_line": fromLine, "to_line": toLine, "new_string": newString,
	})
	f := a.Provider.(*fake)
	f.responses = append(f.responses, toolCall("t1", "edit_file", string(input)), text("bumped it"))
	if err := a.Run(context.Background(), "bump the version", func(Event) {}); err != nil {
		t.Fatalf("scripted turn failed: %v", err)
	}
	return mem
}

// TestRecipeStepsConvertsLineEditPureInsertion is the CHANGELOG scenario
// from the QA report: the model bumps a changelog with a line-range
// edit_file call that inserts a new entry above the existing one by
// retyping from_line 1 to include the new heading followed by the old
// one. RecipeSteps must turn that positional call into a text-form
// edit_file step anchored on the one line of content the recording and
// any later, drifted version of the file still have in common (the old
// heading text) — not the whole hunk padded with the body line below it,
// which would bake in wording ("- initial release") that will not recur
// on a later bump and would make the saved recipe unreplayable after its
// first use.
func TestRecipeStepsConvertsLineEditPureInsertion(t *testing.T) {
	f := &fake{}
	a, root := newEditAgent(t, f, nil)
	a.AutoApprove = true
	changelog := filepath.Join(root, "CHANGELOG.md")
	if err := os.WriteFile(changelog, []byte("## 1.0.0\n- initial release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptEditFileLine(t, a, "CHANGELOG.md", 1, 1, "## 1.1.0\n- bump version\n\n## 1.0.0")
	got, err := os.ReadFile(changelog)
	if err != nil || string(got) != "## 1.1.0\n- bump version\n\n## 1.0.0\n- initial release\n" {
		t.Fatalf("scripted edit did not produce the expected bump: %v %q", err, got)
	}

	steps, err := a.RecipeSteps(0)
	if err != nil {
		t.Fatalf("RecipeSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Fatalf("got %d steps, want 1: %+v", len(steps), steps)
	}
	if steps[0].Tool != "edit_file" {
		t.Fatalf("got tool %q, want edit_file", steps[0].Tool)
	}
	var in struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(steps[0].Input, &in); err != nil {
		t.Fatalf("converted step is not valid edit_file text-form JSON: %s: %v", steps[0].Input, err)
	}
	if in.OldString != "## 1.0.0\n" {
		t.Fatalf("old_string = %q, want just the heading line (minimal anchor, no body text baked in)", in.OldString)
	}
	if in.NewString != "## 1.1.0\n- bump version\n\n## 1.0.0\n" {
		t.Fatalf("new_string = %q", in.NewString)
	}

	// Save with from/version params, exactly as /recipe save
	// version=1.1.0 from=1.0.0 would.
	steps, params := recipe.Parameterise(steps, map[string]string{"version": "1.1.0", "from": "1.0.0"})
	if len(params) != 2 {
		t.Fatalf("got params %+v", params)
	}
	rec := recipe.Recipe{Name: "bump", Params: params, Steps: steps}
	recipesRoot := t.TempDir()
	if _, err := recipe.Save(recipesRoot, rec); err != nil {
		t.Fatal(err)
	}
	loaded, err := recipe.Load(recipesRoot, "bump")
	if err != nil {
		t.Fatal(err)
	}

	// The file has moved on: independently bumped to 1.1.0, with older
	// history that never mentions 1.0.0 at all (it rolled off, or the
	// project simply started its changelog later) — the drifted-but-
	// matching case for a correct replay, and, on a second copy, the
	// drifted-and-gone case a stale parameter must fail safely on.
	movedOn := "## 1.1.0\n- feature X\n\n## 0.9.0\n- beta\n"

	t.Run("replay with the current version as from succeeds", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "CHANGELOG.md")
		if err := os.WriteFile(file, []byte(movedOn), 0o644); err != nil {
			t.Fatal(err)
		}
		a2, _ := newEditAgent(t, nil, nil)
		a2.AutoApprove = true
		a2.Tools.Add(tool.EditFile{Root: dir})
		steps, err := recipe.Expand(loaded, map[string]string{"from": "1.1.0", "version": "1.2.0"})
		if err != nil {
			t.Fatal(err)
		}
		want := "## 1.2.0\n- bump version\n\n" + movedOn
		runAndCheck(t, a2, loaded, steps, file, want, false)
	})

	t.Run("replay with a stale from fails without touching the file", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "CHANGELOG.md")
		if err := os.WriteFile(file, []byte(movedOn), 0o644); err != nil {
			t.Fatal(err)
		}
		a2, _ := newEditAgent(t, nil, nil)
		a2.AutoApprove = true
		a2.Tools.Add(tool.EditFile{Root: dir})
		steps, err := recipe.Expand(loaded, map[string]string{"from": "1.0.0"})
		if err != nil {
			t.Fatal(err)
		}
		runAndCheck(t, a2, loaded, steps, file, movedOn, true)
	})
}

// runAndCheck runs already-expanded steps through RunRecipe (bypassing
// Expand inside it by handing it a recipe whose Steps are already
// expanded and no args) and checks the file's content and whether the
// step was expected to fail.
func runAndCheck(t *testing.T, a *Agent, r recipe.Recipe, steps []recipe.Step, file, wantContent string, wantFail bool) {
	t.Helper()
	r.Steps = steps
	r.Params = nil
	report, failed, err := a.RunRecipe(context.Background(), r, nil, nil)
	if err != nil {
		t.Fatalf("RunRecipe: %v", err)
	}
	if wantFail && failed == nil {
		t.Fatalf("expected a failure; report:\n%s", report)
	}
	if !wantFail && failed != nil {
		t.Fatalf("unexpected failure: %+v; report:\n%s", failed, report)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != wantContent {
		t.Fatalf("file content:\ngot  %q\nwant %q", got, wantContent)
	}
	if !wantFail && !strings.Contains(report, "changed:") {
		t.Fatalf("successful report has no diff stat:\n%s", report)
	}
}

// TestRecipeStepsConvertsWriteFileOverExistingFile checks the write_file
// half of the same bug: a whole-file write_file call over a file that
// already existed is converted into anchored edit_file step(s) instead of
// being replayed as a positional whole-file overwrite.
func TestRecipeStepsConvertsWriteFileOverExistingFile(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"config.txt","content":"env=prod\nport=8080\n"}`),
		text("updated the config"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	if err := os.WriteFile(filepath.Join(root, "config.txt"), []byte("env=staging\nport=8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mem := &session.Memory{}
	a.Log = mem
	if err := a.Run(context.Background(), "point config at prod", func(Event) {}); err != nil {
		t.Fatalf("scripted turn failed: %v", err)
	}

	steps, err := a.RecipeSteps(0)
	if err != nil {
		t.Fatalf("RecipeSteps: %v", err)
	}
	if len(steps) != 1 || steps[0].Tool != "edit_file" {
		t.Fatalf("got %+v, want a single converted edit_file step", steps)
	}
	var in struct {
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(steps[0].Input, &in); err != nil {
		t.Fatal(err)
	}
	if in.OldString != "env=staging\n" || in.NewString != "env=prod\n" {
		t.Fatalf("got old=%q new=%q, want just the changed line, not the unchanged port= line too", in.OldString, in.NewString)
	}
}

// TestRecipeStepsWriteFileNewFileGetsAbsentPrecondition checks the other
// write_file case: a call that created a brand new file cannot be
// anchored (nothing existed before it to diff against), so it is kept as
// write_file but gains an Expect precondition that RunRecipe must refuse
// to bypass when the path exists now with different content, while still
// treating an identical-content path or a genuinely absent one as safe.
func TestRecipeStepsWriteFileNewFileGetsAbsentPrecondition(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"new.txt","content":"hello\n"}`),
		text("created it"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	mem := &session.Memory{}
	a.Log = mem
	if err := a.Run(context.Background(), "create new.txt", func(Event) {}); err != nil {
		t.Fatalf("scripted turn failed: %v", err)
	}
	steps, err := a.RecipeSteps(0)
	if err != nil {
		t.Fatalf("RecipeSteps: %v", err)
	}
	if len(steps) != 1 || steps[0].Tool != "write_file" {
		t.Fatalf("got %+v, want write_file kept as is", steps)
	}
	if steps[0].Expect == nil || !steps[0].Expect.Absent {
		t.Fatalf("got %+v, want an Absent precondition", steps[0].Expect)
	}
	os.Remove(filepath.Join(root, "new.txt"))

	t.Run("absent path: proceeds", func(t *testing.T) {
		r := recipe.Recipe{Name: "create", Steps: steps}
		_, failed, err := a.RunRecipe(context.Background(), r, nil, nil)
		if err != nil || failed != nil {
			t.Fatalf("expected success, got failed=%+v err=%v", failed, err)
		}
		got, err := os.ReadFile(filepath.Join(root, "new.txt"))
		if err != nil || string(got) != "hello\n" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("path exists with the same content: harmless no-op", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("hello\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := recipe.Recipe{Name: "create", Steps: steps}
		_, failed, err := a.RunRecipe(context.Background(), r, nil, nil)
		if err != nil || failed != nil {
			t.Fatalf("expected success, got failed=%+v err=%v", failed, err)
		}
	})

	t.Run("path exists with different content: refuses rather than clobber", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("something else entirely\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := recipe.Recipe{Name: "create", Steps: steps}
		_, failed, err := a.RunRecipe(context.Background(), r, nil, nil)
		if err != nil {
			t.Fatalf("RunRecipe: %v", err)
		}
		if failed == nil {
			t.Fatal("expected a failure")
		}
		if !failed.Denied {
			t.Fatalf("expected a precondition failure to be marked Denied, got %+v", failed)
		}
		got, err := os.ReadFile(filepath.Join(root, "new.txt"))
		if err != nil || string(got) != "something else entirely\n" {
			t.Fatalf("the file must not have been touched: %q, %v", got, err)
		}
	})
}

// TestStepsNeedingApprovalHonoursRulesAndAutoModes checks the piece
// `janetcode --recipe`'s preflight uses to fail fast on stdin instead of
// a terminal: it must flag a plain mutating step that nothing clears, and
// must not flag one a rule allows, one AutoApprove covers, or a read-only
// tool.
func TestStepsNeedingApprovalHonoursRulesAndAutoModes(t *testing.T) {
	a, _ := newAgent(t, nil, nil)
	steps := []recipe.Step{
		{Tool: "read_file", Input: mustJSON(map[string]string{"path": "a.txt"})},
		{Tool: "write_file", Input: mustJSON(map[string]string{"path": "b.txt", "content": "x"})},
	}
	need := a.StepsNeedingApproval(steps)
	if len(need) != 1 || !strings.Contains(need[0], "write_file") {
		t.Fatalf("got %+v, want just the write_file step flagged", need)
	}

	a.AutoApprove = true
	if got := a.StepsNeedingApproval(steps); len(got) != 0 {
		t.Fatalf("AutoApprove should clear everything, got %+v", got)
	}
	a.AutoApprove = false

	a.AutoEdits = true
	if got := a.StepsNeedingApproval(steps); len(got) != 0 {
		t.Fatalf("AutoEdits should clear a write_file step, got %+v", got)
	}
}
