package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/tool"
)

// mutatingRecipe is a one-step recipe whose step is a plain mutating call
// (write_file) that nothing in a bare agent's configuration would already
// clear.
func mutatingRecipe(t *testing.T, root string) recipe.Recipe {
	t.Helper()
	return recipe.Recipe{Name: "touch", Steps: []recipe.Step{
		{Tool: "write_file", Input: mustJSONBytes(t, map[string]string{"path": "a.txt", "content": "hi\n"})},
	}}
}

func mustJSONBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestPreflightRecipeRefusesWhenStdinCannotAnswer is the regression test
// for the HEADLESS WITHOUT APPROVAL bug: `janetcode --recipe` without
// --yolo and no terminal on stdin used to start the run anyway, let every
// mutating step auto-deny, and then still hand the failure to the model
// (whose own retry was denied the same way) — tokens spent for nothing.
// preflightRecipe must refuse before RunRecipe ever runs, whenever stdin
// cannot answer and nothing else (a rule, an auto mode, --yolo) already
// clears the step.
func TestPreflightRecipeRefusesWhenStdinCannotAnswer(t *testing.T) {
	root := t.TempDir()
	ag := &agent.Agent{Tools: tool.NewRegistry(tool.WriteFile{Root: root})}
	r := mutatingRecipe(t, root)

	var stderr bytes.Buffer
	err := preflightRecipe(ag, r, nil, nil, &stderr)
	if err == nil {
		t.Fatal("expected preflightRecipe to refuse: stdin cannot answer and nothing clears the step")
	}
	if !strings.Contains(stderr.String(), "write_file") || !strings.Contains(stderr.String(), "--yolo") {
		t.Fatalf("stderr should name the step and suggest --yolo, got:\n%s", stderr.String())
	}

	t.Run("AutoApprove clears it", func(t *testing.T) {
		ag := &agent.Agent{Tools: tool.NewRegistry(tool.WriteFile{Root: root}), AutoApprove: true}
		var stderr bytes.Buffer
		if err := preflightRecipe(ag, r, nil, nil, &stderr); err != nil {
			t.Fatalf("AutoApprove should clear the preflight check, got: %v", err)
		}
	})

	t.Run("a permission rule clears it", func(t *testing.T) {
		ag := &agent.Agent{Tools: tool.NewRegistry(tool.WriteFile{Root: root})}
		rule, err := perm.Parse("write_file")
		if err != nil {
			t.Fatal(err)
		}
		ag.Rules.Add(perm.Allowed, rule)
		var stderr bytes.Buffer
		if err := preflightRecipe(ag, r, nil, nil, &stderr); err != nil {
			t.Fatalf("an allow rule should clear the preflight check, got: %v", err)
		}
	})

	t.Run("a read-only recipe needs nothing", func(t *testing.T) {
		ag := &agent.Agent{Tools: tool.NewRegistry(tool.ReadFile{Root: root})}
		ro := recipe.Recipe{Name: "peek", Steps: []recipe.Step{
			{Tool: "read_file", Input: mustJSONBytes(t, map[string]string{"path": "a.txt"})},
		}}
		var stderr bytes.Buffer
		if err := preflightRecipe(ag, ro, nil, nil, &stderr); err != nil {
			t.Fatalf("a read-only recipe should never need approval, got: %v", err)
		}
	})
}
