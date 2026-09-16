package memory

import (
	"strings"
	"testing"
)

// A correction closes the entry it supersedes: the new knowledge lands
// under its own name, the old file stays on disk marked with when it
// stopped being true and what replaced it, and it leaves the index and
// recall while staying listed.
func TestReplacesClosesTheOldEntry(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "test-command", Description: "Tests run with go test ./...", Body: "old", Source: "auto"})
	changed, err := s.Apply([]Extracted{{
		Type: "project", Name: "test-command-via-make", Description: "Tests run with make test, not go test",
		Body: "The Makefile's target sets flags the plain command does not.", Replaces: "test-command",
	}})
	if err != nil || len(changed) != 1 {
		t.Fatalf("apply: %+v %v", changed, err)
	}
	old, ok := s.Get("test-command")
	if !ok || !old.Closed() || old.SupersededBy != "test-command-via-make" || old.ValidUntil.IsZero() {
		t.Fatalf("old entry = %+v", old)
	}
	if strings.Contains(s.Index(), "test-command") && !strings.Contains(s.Index(), "test-command-via-make") {
		t.Fatalf("index still advertises the closed entry:\n%s", s.Index())
	}
	if got := s.Recall("what is the test command", 4); len(got) != 1 || got[0].Name != "test-command-via-make" {
		t.Fatalf("recall = %+v, want only the correction", got)
	}
	// Closed stays listed and says so.
	if r := Render(old); !strings.Contains(r, "superseded_by=\"test-command-via-make\"") {
		t.Fatalf("render = %q", r)
	}
	// Re-applying the same correction does not close twice or error.
	if _, err := s.Apply([]Extracted{{
		Type: "project", Name: "test-command-via-make", Description: "Tests run with make test, not go test",
		Body: "The Makefile's target sets flags the plain command does not.", Replaces: "test-command",
	}}); err != nil {
		t.Fatal(err)
	}
}

// A replaces that names the entry's own name is a plain update, not a
// close: the extractor often corrects an entry in place.
func TestReplacesSameNameIsAPlainUpdate(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "commit-size", Description: "Commits under 100 lines", Body: "old", Source: "auto"})
	if _, err := s.Apply([]Extracted{{
		Type: "feedback", Name: "commit-size", Description: "Commits under 50 lines", Body: "new", Replaces: "commit-size",
	}}); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get("commit-size")
	if e.Closed() || e.Description != "Commits under 50 lines" {
		t.Fatalf("entry = %+v", e)
	}
}

// Evidence the harness stamps survives the file round-trip, and Put's
// merge unions it across an update.
func TestEvidenceRoundTripsAndMerges(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "n", Description: "A noted fact", Body: "b", Source: "auto", Evidence: []string{"sess1#~9"}})
	e, _ := s.Get("n")
	if len(e.Evidence) != 1 || e.Evidence[0] != "sess1#~9" {
		t.Fatalf("evidence = %v", e.Evidence)
	}
	s.Put(Entry{Name: "n", Description: "A noted fact, expanded", Body: "b2", Source: "auto", Evidence: []string{"sess2#~14"}})
	e, _ = s.Get("n")
	if strings.Join(e.Evidence, ",") != "sess1#~9,sess2#~14" {
		t.Fatalf("merged evidence = %v", e.Evidence)
	}
}

// An entry about a path is recalled by a turn that names the path, even
// when no word of its text overlaps.
func TestConcernsRecallThroughTheWork(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{
		Name: "compact-threshold", Description: "Compaction threshold raised for long sessions", Body: "Set compact_at above 80k only when the task runs long.",
		Source: "auto", Concerns: []string{"internal/agent/compact.go"},
	})
	got := s.Recall("change how internal/agent/compact.go prunes", 4)
	if len(got) != 1 || got[0].Name != "compact-threshold" {
		t.Fatalf("recall = %+v", got)
	}
	// One passing word that is not a concern does not admit it.
	if got := s.Recall("compact the output please", 4); len(got) != 0 {
		t.Fatalf("weak concern signal recalled %+v", got)
	}
}
