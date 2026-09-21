package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/jev"
)

// A labeled corpus is only worth what its labels are. These are the
// properties the replay silently assumes: every row parses, says which
// consumer's questions it is labeled for, carries a truth that is one
// of the answers that consumer can give, and is named once so a run's
// rows can be compared with an earlier run's.
func corpusRows(t *testing.T, path string) []labeledState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []labeledState
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r labeledState
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("%s line %d: %v", path, i+1, err)
		}
		rows = append(rows, r)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Name == "" || r.Truth == "" || strings.TrimSpace(r.State) == "" {
			t.Fatalf("%s: incomplete row %+v", path, r)
		}
		if seen[r.Name] {
			t.Fatalf("%s: duplicate row name %q", path, r.Name)
		}
		seen[r.Name] = true
	}
	return rows
}

func TestTriageCorpusIsWellFormed(t *testing.T) {
	rows := corpusRows(t, "../../docs/benchmarks/decisions/triage.jsonl")
	truths := map[string]int{}
	for _, r := range rows {
		if r.Consumer != "" {
			t.Fatalf("triage rows carry no consumer field (got %q)", r.Consumer)
		}
		truths[r.Truth]++
	}
	// The mapping's costly mistake is a skip on a row that wanted
	// repair, so the corpus has to contain rows of both kinds.
	for _, want := range []string{"patch_caused", "environmental", "flaky", "unrelated", "inconclusive"} {
		if truths[want] == 0 {
			t.Fatalf("no %s rows: the corpus cannot score that cell", want)
		}
	}
}

func TestIntakeCorpusIsWellFormed(t *testing.T) {
	rows := corpusRows(t, "../../docs/benchmarks/decisions/intake.jsonl")
	var options map[string]string
	for _, q := range agent.IntakeQuestions() {
		if q.Kind == jev.Choice {
			options = q.Options
		}
	}
	truths := map[string]int{}
	for _, r := range rows {
		// The corpus says which questions it is labeled for; the driver
		// dispatches on it, and a triage corpus scored with intake
		// questions would report nonsense with a straight face.
		if r.Consumer != "intake" {
			t.Fatalf("row %q: consumer = %q, want intake", r.Name, r.Consumer)
		}
		if _, ok := options[r.Truth]; !ok {
			t.Fatalf("row %q: truth %q is not one of the kind options %v", r.Name, r.Truth, options)
		}
		truths[r.Truth]++
	}
	for kind := range options {
		if truths[kind] < 3 {
			t.Fatalf("kind %q has %d rows; a cell under three rows measures nothing", kind, truths[kind])
		}
	}
}
