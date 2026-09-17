package memory

import (
	"context"
	"strings"
	"testing"
)

// The consolidation call cites the record lines it stands on; citations
// that name lines the record does not have are rejected, and a candidate
// with no citation at all is rejected with it.
func TestSleepValidatesEvidenceAgainstTheRecord(t *testing.T) {
	in := SleepInput{
		SessionID: "sess1", Goal: "fix the parser", GoalOutcome: "complete",
		Facts: []SleepFact{
			{Seq: 3, Kind: "user", Text: "fix the parser"},
			{Seq: 7, Kind: "verify", Text: "go test failed: TestParse"},
			{Seq: 9, Kind: "verdict", Text: "pass after fixing the lexer first"},
		},
		Touched: []string{"parser.go"},
	}
	f := &fakeProvider{reply: `{"type":"project","name":"lexer-first","description":"Parser fixes start in the lexer","body":"The verify failure at seq 7 was the lexer, not the parser.","concerns":["parser.go"],"evidence":[7,9]}` + "\n" +
		`{"type":"project","name":"invented","description":"A claim with no real citation","body":"cites a line the record does not have","evidence":[42]}` + "\n" +
		`{"type":"project","name":"uncited","description":"A claim with no citation","body":"no evidence field at all"}`}
	xs, err := Sleep(context.Background(), f, "m", in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(xs) != 1 || xs[0].Name != "lexer-first" {
		t.Fatalf("candidates = %+v", xs)
	}
	if strings.Join(xs[0].Evidence, ",") != "sess1#7,sess1#9" {
		t.Fatalf("evidence = %v", xs[0].Evidence)
	}
	// The record the model read names the session, the goal, the changed
	// files and every line's seq.
	ask := f.req.Messages[len(f.req.Messages)-1].Text()
	for _, want := range []string{"sess1", "fix the parser", "complete", "parser.go", "[7] (verify)", "[9] (verdict)"} {
		if !strings.Contains(ask, want) {
			t.Fatalf("record missing %q:\n%s", want, ask)
		}
	}
	if !strings.Contains(f.req.System, "evidence") {
		t.Fatal("the sleep prompt does not require evidence")
	}
}

// A sleep pass writes through the same guarded path as extraction:
// project scope, its own source, evidence kept, concerns clipped.
func TestSleepEntriesApplyWithEvidenceAndConcerns(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	xs := []Extracted{{
		Type: "project", Name: "flaky-gate", Description: "The integration gate is flaky under load",
		Body:     "go test -race ./internal/gate times out on a loaded machine; rerun before editing.",
		Concerns: []string{"internal/gate/gate.go"}, Evidence: []string{"sess1#12"},
	}}
	changed, err := s.ApplyWith(xs, "sleep")
	if err != nil || len(changed) != 1 {
		t.Fatalf("apply: %+v %v", changed, err)
	}
	e, _ := s.Get("flaky-gate")
	if e.Source != "sleep" || e.Scope != "project" || len(e.Evidence) != 1 || e.Concerns[0] != "internal/gate/gate.go" {
		t.Fatalf("entry = %+v", e)
	}
	// And the concern reaches recall: a turn naming the file finds it.
	if got := s.Recall("work on internal/gate/gate.go today", 4); len(got) != 1 {
		t.Fatalf("recall = %+v", got)
	}
}

// An empty record is no call at all.
func TestSleepSkipsAnEmptyRecord(t *testing.T) {
	f := &fakeProvider{reply: "NONE"}
	xs, err := Sleep(context.Background(), f, "m", SleepInput{SessionID: "s"}, nil)
	if err != nil || len(xs) != 0 || f.req.Model != "" {
		t.Fatalf("xs=%v err=%v called=%v", xs, err, f.req.Model != "")
	}
}
