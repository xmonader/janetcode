package repomap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BlastRadius must not disturb the index it queries. It used to hand the
// index's own definers slice to an in-place uniq, so asking twice about a
// symbol several files define could give a different answer the second
// time -- and the graph stayed rewritten for every later query.
func TestBlastRadiusDoesNotMutateTheIndex(t *testing.T) {
	dir := t.TempDir()
	// Three files define Handle; a fourth uses it.
	for i, name := range []string{"a.go", "b.go", "c.go"} {
		src := "package p\n\nfunc Handle() int { return " + string(rune('0'+i)) + " }\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "use.go"), []byte("package p\n\nfunc Caller() int { return Handle() }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ix, err := Build(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]int(nil), ix.definers["Handle"]...)

	first, ok := ix.BlastRadius("Handle", false)
	if !ok {
		t.Fatal("Handle has no blast radius")
	}
	if len(first.Definers) != 3 {
		t.Fatalf("definers = %v, want three files", first.Definers)
	}
	after := ix.definers["Handle"]
	if len(after) != len(before) {
		t.Fatalf("definers[Handle] length changed: %v -> %v", before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("definers[Handle] rewritten: %v -> %v", before, after)
		}
	}

	second, ok := ix.BlastRadius("Handle", false)
	if !ok || len(second.Definers) != len(first.Definers) {
		t.Fatalf("second lookup = %v, first = %v", second.Definers, first.Definers)
	}
}

// Render must stay inside its budget, footer included: the map is
// attached to the user turn under a fixed allowance, and the "N more
// files" line used to be written after the budget check rather than
// reserved from it.
func TestRenderStaysWithinBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 40; i++ {
		name := filepath.Join(dir, fmt.Sprintf("file%02d.go", i))
		src := fmt.Sprintf("package p\n\nfunc SomeReasonablyLongSymbolName%02d() {}\n", i)
		if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := Build(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ranked := ix.Rank("something", nil)
	for _, budget := range []int{400, 800, 1500, 6000} {
		if got := ix.Render(ranked, budget); len(got) > budget {
			t.Errorf("budget %d: rendered %d bytes:\n%s", budget, len(got), got)
		}
	}
}
