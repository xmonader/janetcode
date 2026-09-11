package memory

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIndexBudgetIsAShareOfTheWindow: a flat 8 KB of index is a
// twenty-fifth of a 200k context window and a sixteenth of a 32k one.
// The budget is the same share of either, and never more than the flat
// ceiling.
func TestIndexBudgetIsAShareOfTheWindow(t *testing.T) {
	cases := []struct{ window, want int }{
		{0, IndexBudget},         // unknown: keep the ceiling
		{1_000_000, IndexBudget}, // 80 KB of share, capped
		{200_000, IndexBudget},
		{128_000, IndexBudget},
		{64_000, 5120},
		{32_000, 2560},
		{8_000, 1024}, // the floor: the entries the user wrote stay visible
	}
	for _, c := range cases {
		if got := BudgetFor(c.window); got != c.want {
			t.Errorf("BudgetFor(%d) = %d, want %d", c.window, got, c.want)
		}
	}
}

// TestIndexStopsAtItsBudgetAndSaysSo: what does not fit is not silently
// gone — the model is told how many entries recall can still reach.
func TestIndexStopsAtItsBudgetAndSaysSo(t *testing.T) {
	s := &Store{Root: t.TempDir(), Key: "p"}
	// Written straight to disk rather than through Put, which merges
	// entries whose descriptions resemble each other: forty entries that
	// differ only by a number are one entry as far as Put is concerned.
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("entry-with-a-long-enough-name-%02d", i)
		e := Entry{
			Name:        name,
			Description: fmt.Sprintf("%s %s", name, strings.Repeat("padding to make the line worth measuring ", 2)),
			Type:        TypeProject, Scope: "project", Body: "b",
			Created: time.Now(), Updated: time.Now(),
			Path: filepath.Join(s.dir("project"), name+".md"),
		}
		if err := s.write(e); err != nil {
			t.Fatal(err)
		}
	}
	full := s.IndexWithin(IndexBudget)
	small := s.IndexWithin(1024)
	if len(small) >= len(full) {
		t.Fatalf("a smaller budget did not shrink the index: %d vs %d", len(small), len(full))
	}
	if len(small) > 1024+120 {
		t.Fatalf("the index is %d bytes for a budget of 1024", len(small))
	}
	if !strings.Contains(small, "more; memory recall finds them") {
		t.Fatalf("the index does not say what it left out:\n%s", small)
	}
}
