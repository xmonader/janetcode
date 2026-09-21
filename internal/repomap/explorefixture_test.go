package repomap

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The deterministic baseline for context pre-selection, measured on the one
// realistically sized fixture this repository has: evals/explore/61, 59
// files, whose task has exactly two right answers — the file that defines
// the refund-window constant and the one file that hard-codes its value.
//
// This is the number any Jev reranker has to beat before it is worth a
// call, so it is pinned here rather than left in a notes file: if the
// ranker's placement of those two files changes, this fails and the
// baseline in docs/notes/jev.md is stale.
//
// The rank positions asserted below are ceilings, not exact values: a
// change that moves an answer file *up* is an improvement and passes.
const (
	exploreTask      = "../../evals/explore/61-explore-large-repo.json"
	exploreAnswerOne = "billing/limits.go"
	exploreAnswerTwo = "report/audit.go"
	// exploreRankCeiling is the top-k the worse-placed of the two answers sits
	// inside today (it is at 9 of 58). A ranker with no signal would average 29.
	exploreRankCeiling = 10
)

func TestRepoMapBaselineOnTheExploreFixture(t *testing.T) {
	data, err := os.ReadFile(exploreTask)
	if err != nil {
		t.Skipf("fixture not present: %v", err)
	}
	var task struct {
		Prompt string            `json:"prompt"`
		Files  map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if len(task.Files) < 50 {
		t.Fatalf("fixture has %d files; this baseline is only meaningful on a large one", len(task.Files))
	}

	root := t.TempDir()
	for rel, body := range task.Files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ix, err := Build(context.Background(), root, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	ranked := ix.Rank(task.Prompt, nil)
	if len(ranked) < 2 {
		t.Fatalf("ranked %d files", len(ranked))
	}
	at := map[string]int{}
	for i, r := range ranked {
		at[r.File.Path] = i + 1
	}
	one, okOne := at[exploreAnswerOne]
	two, okTwo := at[exploreAnswerTwo]
	if !okOne || !okTwo {
		t.Fatalf("answer files missing from the ranking: %s=%v %s=%v",
			exploreAnswerOne, okOne, exploreAnswerTwo, okTwo)
	}
	worst := one
	if two > worst {
		worst = two
	}
	t.Logf("deterministic repo-map baseline over %d files: %s at %d, %s at %d",
		len(ranked), exploreAnswerOne, one, exploreAnswerTwo, two)
	if worst > exploreRankCeiling {
		t.Errorf("the worse-placed answer is at rank %d of %d, past the recorded ceiling %d; "+
			"update docs/notes/jev.md's item 1 baseline with the new number",
			worst, len(ranked), exploreRankCeiling)
	}
}
