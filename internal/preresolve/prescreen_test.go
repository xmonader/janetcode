package preresolve

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The offline pre-screen the idea names: the 58-file explore fixture
// (evals/explore/61), where 40–54% of a live run went by before the first
// edit (docs/notes/jev.md, "Step 0 on a 58-file repository"). Its task
// names no file and no identifier — "a single constant defines how long a
// refund may be requested" — so only the word tier can resolve anything,
// and the second answer (the file that hard-codes the number instead of
// using the constant) cannot be found by any name at all: it does not
// reference the constant, which is the bug.
//
// What this pins: the block carries the constant's definition, whose
// comment names the other file, and nothing from the five report/ files
// the request's word "report" pulls to the top of the map. The live
// measure — calls before the first edit, with the drop under 20% as the
// kill line — needs a run; this is the part that can be checked here.
func TestPreresolvePrescreenExploreFixture(t *testing.T) {
	data, err := os.ReadFile("../../evals/explore/61-explore-large-repo.json")
	if err != nil {
		t.Skipf("fixture not present: %v", err)
	}
	var task struct {
		Prompt string            `json:"prompt"`
		Files  map[string]string `json:"files"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	ix := preresolveRepo(t, task.Files)
	res := Resolve(ix, task.Prompt, 0)
	t.Logf("named: %d mention(s); resolved %d definition(s) %v, %d test(s), %d byte(s)", len(Mentions(task.Prompt)), len(res.Symbols), res.Symbols, len(res.Tests), len(res.Text))
	if !strings.Contains(res.Text, `symbol="RefundWindowDays" path="billing/limits.go"`) {
		t.Fatalf("the refund constant was not resolved:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "see report/audit.go") {
		t.Fatalf("the definition's comment, which names the second file, was cut:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "report/report") {
		t.Fatalf("a junk report file was resolved:\n%s", res.Text)
	}
	if len(res.Text) > 1024 {
		t.Fatalf("%d bytes for one constant: the block should be small when the request names little", len(res.Text))
	}
}
