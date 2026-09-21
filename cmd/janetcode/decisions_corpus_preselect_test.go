package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestPreselectCorpusIsWellFormed guards the by-construction corpus the
// repo-map precision work starts from: every row must name the consumer,
// carry a ranking long enough for a top-k to mean anything, and carry at
// least one file the run actually touched, or the row cannot be scored.
func TestPreselectCorpusIsWellFormed(t *testing.T) {
	data, err := os.ReadFile("../../docs/benchmarks/decisions/preselect.jsonl")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	var rows int
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r struct {
			Name         string   `json:"name"`
			Model        string   `json:"model"`
			Consumer     string   `json:"consumer"`
			FilesIndexed int      `json:"files_indexed"`
			Ranked       []string `json:"ranked"`
			Truth        []string `json:"truth_touched"`
		}
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			t.Fatalf("row %d: %v", rows, err)
		}
		rows++
		if r.Consumer != "preselect" {
			t.Errorf("%s: consumer %q", r.Name, r.Consumer)
		}
		if r.Name == "" || r.Model == "" {
			t.Errorf("row %d: name %q model %q", rows, r.Name, r.Model)
		}
		if r.FilesIndexed < 50 {
			t.Errorf("%s: %d files indexed; this corpus is only meaningful on a large repository", r.Name, r.FilesIndexed)
		}
		if len(r.Ranked) < 10 {
			t.Errorf("%s: ranking of %d is too short to score a top-k", r.Name, len(r.Ranked))
		}
		if len(r.Truth) == 0 {
			t.Errorf("%s: no touched files, so the row cannot be scored", r.Name)
		}
	}
	if rows == 0 {
		t.Fatal("corpus is empty")
	}
}
