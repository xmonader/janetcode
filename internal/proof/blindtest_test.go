package proof

import (
	"strings"
	"testing"
)

func TestBlindTestLine(t *testing.T) {
	for _, tc := range []struct {
		b    BlindTest
		want string
	}{
		{BlindTest{Skipped: "no checks in the reply"}, "blind tester: skipped (no checks in the reply)"},
		{BlindTest{Checks: 4, Held: 4}, "blind tester: 4/4 rules held"},
		{BlindTest{Checks: 4, Held: 2, Inconclusive: 1, Violated: []string{"r"}, SentBack: true}, "blind tester: 2/4 rules held, 1 failed, 1 inconclusive"},
		{BlindTest{Checks: 2, Held: 1, Violated: []string{"r"}, Rechecked: true}, "blind tester: 1/2 rules held, 1 failed; all fixed after one round"},
		{BlindTest{Checks: 2, Held: 1, Violated: []string{"r"}, Rechecked: true, Still: []string{"r"}}, "blind tester: 1/2 rules held, 1 failed; 1 still failing after one round"},
	} {
		if got := tc.b.Line(); got != tc.want {
			t.Errorf("Line = %q, want %q", got, tc.want)
		}
	}
	if s := (BlindTest{Checks: 1, Violated: []string{"ties break by id"}}).String(); !strings.HasSuffix(s, "\n  - ties break by id") {
		t.Fatalf("String = %q", s)
	}
}

// The record reaches the turn in the note and its summary.
func TestBuildCarriesTheBlindTest(t *testing.T) {
	bt := &BlindTest{Model: "cheap", Checks: 3, Held: 3}
	r := Build("s", "m", "/r",
		[]ChangeInput{{Seq: 1, Turn: 1, Path: "a.go", Before: []byte("x"), After: []byte("y")}},
		map[int]TurnOutcomeInput{1: {Verdict: "verified", BlindTest: bt}},
		map[string]FileVersions{"a.go": {Parent: []byte("x"), Commit: []byte("y")}})
	if len(r.Turns) != 1 || r.Turns[0].BlindTest != bt {
		t.Fatalf("turns = %+v", r.Turns)
	}
	if !strings.Contains(Summary(r), "· blind tester: 3/3 rules held") {
		t.Fatalf("summary:\n%s", Summary(r))
	}
}
