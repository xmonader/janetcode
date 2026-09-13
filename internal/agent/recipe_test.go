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
	"github.com/xmonader/janetcode/internal/verify"
)

// TestRecipeSaveAndReplayWithParam drives a scripted turn that writes a
// file, crystallises it into a recipe the same way /recipe save does
// (FromEvents, Parameterise, Save), reloads it (Load) and replays it with
// a parameter substituted through Agent.RunRecipe — no model call at all
// — then checks the file the replay actually produced.
func TestRecipeSaveAndReplayWithParam(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"config.txt","content":"env=staging\n"}`),
		text("wrote the config"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true // write_file is mutating; nothing here is testing approval
	mem := &session.Memory{}
	a.Log = mem

	if err := a.Run(context.Background(), "write the staging config", func(Event) {}); err != nil {
		t.Fatalf("scripted turn failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "config.txt"))
	if err != nil || string(got) != "env=staging\n" {
		t.Fatalf("turn did not write the file as scripted: %v %q", err, got)
	}

	steps, err := recipe.FromEvents(mem.Events(), 0)
	if err != nil {
		t.Fatalf("FromEvents: %v", err)
	}
	if len(steps) != 1 || steps[0].Tool != "write_file" {
		t.Fatalf("got steps %+v", steps)
	}
	steps, params := recipe.Parameterise(steps, map[string]string{"env": "staging"})
	if len(params) != 1 || params[0].Name != "env" || params[0].Default != "staging" {
		t.Fatalf("got params %+v", params)
	}

	r := recipe.Recipe{Name: "write-config", Description: "writes the config", Params: params, Steps: steps}
	recipesRoot := t.TempDir()
	path, err := recipe.Save(recipesRoot, r)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("recipe file was not written: %v", err)
	}
	loaded, err := recipe.Load(recipesRoot, "write-config")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Replay with the parameter substituted: no model call is involved,
	// only the tool pipeline (authorize, checkpoints, the event log).
	report, failed, err := a.RunRecipe(context.Background(), loaded, map[string]string{"env": "prod"}, nil)
	if err != nil {
		t.Fatalf("RunRecipe: %v", err)
	}
	if failed != nil {
		t.Fatalf("unexpected failure: %+v", failed)
	}
	if report == "" {
		t.Fatal("expected a non-empty report")
	}
	replayed, err := os.ReadFile(filepath.Join(root, "config.txt"))
	if err != nil || string(replayed) != "env=prod\n" {
		t.Fatalf("replay did not substitute the parameter: %v %q", err, replayed)
	}

	// The replay is its own logged turn: a fresh turn/start and turn/end
	// pair, distinct from the scripted turn's.
	var turnStarts int
	for _, e := range mem.Events() {
		if e.Type == session.TypeTurnStart {
			turnStarts++
		}
	}
	if turnStarts != 2 {
		t.Fatalf("got %d turn/start events, want 2 (the scripted turn and the replay)", turnStarts)
	}
}

// TestRecipeStopsAtFirstFailingStep checks that RunRecipe stops at the
// first step whose tool call fails and reports which one, without
// running anything after it.
func TestRecipeStopsAtFirstFailingStep(t *testing.T) {
	a, root := newAgent(t, nil, nil)
	mem := &session.Memory{}
	a.Log = mem

	r := recipe.Recipe{
		Name: "two-reads",
		Steps: []recipe.Step{
			{Tool: "read_file", Input: mustJSON(map[string]string{"path": "a.txt"})},
			{Tool: "read_file", Input: mustJSON(map[string]string{"path": "missing.txt"})},
			{Tool: "write_file", Input: mustJSON(map[string]string{"path": "should-not-exist.txt", "content": "x"})},
		},
	}
	report, failed, err := a.RunRecipe(context.Background(), r, nil, nil)
	if err != nil {
		t.Fatalf("RunRecipe returned an error rather than a RecipeFailure: %v", err)
	}
	if failed == nil {
		t.Fatal("expected a RecipeFailure")
	}
	if failed.Step != 2 || failed.Tool != "read_file" {
		t.Fatalf("got %+v", failed)
	}
	if report == "" {
		t.Fatal("expected a non-empty report even on failure")
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-exist.txt")); err == nil {
		t.Fatal("the step after the failure must not have run")
	}

	handover := RecipeHandover(r, *failed)
	if handover == "" {
		t.Fatal("expected a non-empty handover prompt")
	}
	for _, want := range []string{r.Name, "read_file", "step 2"} {
		if !strings.Contains(handover, want) {
			t.Fatalf("handover %q missing %q", handover, want)
		}
	}
}

// TestRecipeVerifyGateFailureAfterSuccessfulSteps checks that a recipe
// whose steps all succeed but that leaves the project's own checks
// failing is reported the same way a failing step is: a RecipeFailure a
// front-end already knows how to hand to the model, marked Verify so it
// reads as "the recipe worked, the checks didn't" rather than as a
// step that failed. It reuses the exact same verifyGate a model turn
// runs (see TestVerifyGateFeedsFailuresBackThenPasses in hook_test.go for
// that path), so the check here is only that RunRecipe wires it in and
// reports its outcome, not that verifyGate itself behaves.
func TestRecipeVerifyGateFailureAfterSuccessfulSteps(t *testing.T) {
	a, root := newAgent(t, nil, nil)
	a.AutoApprove = true // write_file is mutating
	mem := &session.Memory{}
	a.Log = mem
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"grep -q ok a.txt"}}}

	r := recipe.Recipe{
		Name:        "write-config",
		Description: "writes the config",
		Steps: []recipe.Step{
			{Tool: "write_file", Input: mustJSON(map[string]string{"path": "a.txt", "content": "nope"})},
		},
	}
	report, failed, err := a.RunRecipe(context.Background(), r, nil, nil)
	if err != nil {
		t.Fatalf("RunRecipe returned an error rather than a RecipeFailure: %v", err)
	}
	if failed == nil {
		t.Fatal("expected a RecipeFailure from the verify gate")
	}
	if !failed.Verify || failed.Tool != "verify" || failed.Step != 0 {
		t.Fatalf("got %+v, want a verify failure with Step 0", failed)
	}
	if !strings.Contains(failed.Output, "grep -q ok a.txt") {
		t.Fatalf("failure output = %q, want it to name the failing check", failed.Output)
	}
	if !strings.Contains(report, "verify") {
		t.Fatalf("report %q does not mention the verify step", report)
	}

	handover := RecipeHandover(r, *failed)
	for _, want := range []string{r.Name, "checks failed", "grep -q ok a.txt", r.Description} {
		if !strings.Contains(handover, want) {
			t.Fatalf("handover %q missing %q", handover, want)
		}
	}

	// The step itself ran (write_file wrote the file); only the check
	// after it failed.
	if got, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(got) != "nope" {
		t.Fatalf("step should still have run: %v %q", err, got)
	}
}

// TestNeedsMCP checks the decision `janetcode --recipe` uses to skip
// starting MCP servers for a recipe that never calls one of their tools.
func TestNeedsMCP(t *testing.T) {
	plain := recipe.Recipe{Steps: []recipe.Step{
		{Tool: "read_file", Input: mustJSON(map[string]string{"path": "a.txt"})},
		{Tool: "write_file", Input: mustJSON(map[string]string{"path": "b.txt", "content": "x"})},
	}}
	if NeedsMCP(plain) {
		t.Fatal("a recipe with no mcp__ step should not need MCP servers started")
	}
	withMCP := recipe.Recipe{Steps: []recipe.Step{
		{Tool: "read_file", Input: mustJSON(map[string]string{"path": "a.txt"})},
		{Tool: "mcp__docs__search", Input: mustJSON(map[string]string{"q": "x"})},
	}}
	if !NeedsMCP(withMCP) {
		t.Fatal("a recipe with an mcp__ step should need MCP servers started")
	}
	if NeedsMCP(recipe.Recipe{}) {
		t.Fatal("an empty recipe needs no MCP servers")
	}
}
