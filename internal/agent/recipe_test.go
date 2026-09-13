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
