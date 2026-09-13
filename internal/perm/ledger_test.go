package perm

import (
	"strings"
	"testing"
)

func TestLedgerPersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	l := OpenLedger(dir, "proj")
	l.Record("bash(go test *)", true)
	l.Record("bash(go test *)", true)
	tally := l.Record("bash(go test *)", true)
	if tally.Allowed != 3 || tally.Denied != 0 {
		t.Fatalf("tally = %+v", tally)
	}

	// A fresh ledger over the same directory and project sees it.
	l2 := OpenLedger(dir, "proj")
	got := l2.Get("bash(go test *)")
	if got.Allowed != 3 {
		t.Fatalf("did not persist across OpenLedger: %+v", got)
	}

	// A different project does not.
	l3 := OpenLedger(dir, "other")
	if got := l3.Get("bash(go test *)"); got.Allowed != 0 {
		t.Fatalf("leaked across projects: %+v", got)
	}
}

func TestLedgerRecordsDenials(t *testing.T) {
	dir := t.TempDir()
	l := OpenLedger(dir, "proj")
	l.Record("bash(rm *)", true)
	tally := l.Record("bash(rm *)", false)
	if tally.Allowed != 1 || tally.Denied != 1 {
		t.Fatalf("tally = %+v", tally)
	}
}

func TestLedgerNilAndEmptyAreNoOps(t *testing.T) {
	var nilLedger *Ledger
	if got := nilLedger.Record("bash(go test *)", true); got != (Tally{}) {
		t.Fatalf("nil ledger recorded: %+v", got)
	}
	if got := nilLedger.Get("bash(go test *)"); got != (Tally{}) {
		t.Fatalf("nil ledger returned: %+v", got)
	}
	if got := nilLedger.Candidates(1); got != nil {
		t.Fatalf("nil ledger returned candidates: %v", got)
	}

	empty := OpenLedger("", "")
	if empty == nil {
		t.Fatal("OpenLedger with no path returned nil")
	}
	if got := empty.Record("bash(go test *)", true); got != (Tally{}) {
		t.Fatalf("empty-path ledger recorded: %+v", got)
	}
	if got := empty.Candidates(1); got != nil {
		t.Fatalf("empty-path ledger returned candidates: %v", got)
	}
}

func TestLedgerCandidatesFiltersAndSorts(t *testing.T) {
	dir := t.TempDir()
	l := OpenLedger(dir, "proj")
	for i := 0; i < 3; i++ {
		l.Record("bash(go test *)", true)
	}
	for i := 0; i < 5; i++ {
		l.Record("bash(git status)", true)
	}
	// Refused once: never a candidate, however often it was also allowed.
	l.Record("bash(rm *)", true)
	l.Record("bash(rm *)", true)
	l.Record("bash(rm *)", true)
	l.Record("bash(rm *)", false)
	// Below the threshold.
	l.Record("bash(curl *)", true)

	got := l.Candidates(LearnAfter)
	if len(got) != 2 {
		t.Fatalf("candidates = %+v", got)
	}
	if got[0].Rule != "bash(git status)" || got[0].Allowed != 5 {
		t.Fatalf("first candidate = %+v", got[0])
	}
	if got[1].Rule != "bash(go test *)" || got[1].Allowed != 3 {
		t.Fatalf("second candidate = %+v", got[1])
	}
}

func TestCandidateLinesSkipsAlreadySavedRules(t *testing.T) {
	dir := t.TempDir()
	l := OpenLedger(dir, "proj")
	for i := 0; i < LearnAfter; i++ {
		l.Record("bash(go test *)", true)
		l.Record("bash(git status)", true)
	}
	rules := Rules{}
	rules.Add(Allowed, Rule{Tool: "bash", Pattern: "go test *"})

	lines := CandidateLines(l, rules)
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("lines = %v", lines)
	}
	for _, ln := range lines {
		if ln == "" {
			t.Fatalf("empty line in %v", lines)
		}
	}
	joined := lines[0]
	if want := "bash(git status)"; !strings.Contains(joined, want) {
		t.Fatalf("expected %q to mention %q", joined, want)
	}
	if strings.Contains(strings.Join(lines, "\n"), "go test") {
		t.Fatalf("an already-saved rule was still offered: %v", lines)
	}
}
