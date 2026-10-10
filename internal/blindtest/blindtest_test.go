package blindtest

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseReadsRuleAndBlockPairs(t *testing.T) {
	reply := "Here are the checks.\n\n" +
		"```sh\nstray-block-before-any-rule\n```\n" +
		"1. **RULE:** limit must be 1..100\n```sh\npython3 - <<'EOF'\nimport m\nEOF\n```\n" +
		"RULE: a rule with no block\n" +
		"RULE: ties break by id\n```\nnode -e 'x'\n```\n" +
		"RULE: an empty block is dropped\n```sh\n\n```\n" +
		"Rule: lower-case label\n```bash\ngo test -run TestX ./...\n```\n"
	got := Parse(reply)
	want := []Check{
		{Rule: "limit must be 1..100", Cmd: "python3 - <<'EOF'\nimport m\nEOF"},
		{Rule: "ties break by id", Cmd: "node -e 'x'"},
		{Rule: "lower-case label", Cmd: "go test -run TestX ./..."},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Parse =\n%q\nwant\n%q", got, want)
	}
}

func TestParseKeepsAtMostMaxChecks(t *testing.T) {
	var b strings.Builder
	for i := 0; i < MaxChecks+3; i++ {
		fmt.Fprintf(&b, "RULE: r%d\n```sh\nc%d\n```\n", i, i)
	}
	if got := Parse(b.String()); len(got) != MaxChecks || got[MaxChecks-1].Cmd != fmt.Sprintf("c%d", MaxChecks-1) {
		t.Fatalf("Parse kept %d: %+v", len(got), got)
	}
	if Parse("") != nil || Parse("no rules at all") != nil {
		t.Fatal("checks out of nothing")
	}
	// An unterminated block runs to the end of the reply.
	if got := Parse("RULE: r\n```sh\ncmd"); len(got) != 1 || got[0].Cmd != "cmd" {
		t.Fatalf("unterminated: %+v", got)
	}
}

func TestJudgeTellsAViolationFromACrash(t *testing.T) {
	for _, tc := range []struct {
		ran, failed bool
		output      string
		want        Outcome
	}{
		{true, false, "", Held},
		{true, true, "VIOLATION: want 3, got 2\n[exit code 1]", Violated},
		{true, true, "  VIOLATION: indented still counts", Violated},
		{true, false, "VIOLATION: printed but exited 0", Violated},
		// The checker's own crash: wrong import, a typo, a compile error.
		{true, true, "ModuleNotFoundError: No module named 'catalgo'\n[exit code 1]", Inconclusive},
		{true, true, "./x_test.go:3: undefined: Serch", Inconclusive},
		// The marker in the middle of a line is someone else's text.
		{true, true, "got error: no VIOLATION: here", Inconclusive},
		{false, true, "VIOLATION: never ran", Inconclusive},
	} {
		if got := Judge(tc.ran, tc.failed, tc.output); got != tc.want {
			t.Errorf("Judge(%v, %v, %q) = %v, want %v", tc.ran, tc.failed, tc.output, got, tc.want)
		}
	}
}

func TestPromptHoldsTheSpecAndFileNames(t *testing.T) {
	p := Prompt("  Implement f.  ", []string{"m.py", "test_m.py"})
	if !strings.Contains(p, "TASK:\nImplement f.") || !strings.Contains(p, "m.py\ntest_m.py\n") {
		t.Fatalf("prompt = %q", p)
	}
	long := Prompt(strings.Repeat("x", MaxSpec+100), nil)
	if len(long) > MaxSpec+200 || !strings.Contains(long, "[... task text cut]") || strings.Contains(long, "PROJECT FILES") {
		t.Fatalf("long prompt: %d bytes", len(long))
	}
}

func TestNoteNamesRulesOnly(t *testing.T) {
	n := Note([]string{"limit must be 1..100", "ties break by id"})
	if !strings.Contains(n, "1. limit must be 1..100\n2. ties break by id\n") || !strings.HasPrefix(n, "[harness]") {
		t.Fatalf("note = %q", n)
	}
	if !strings.Contains(n, "The tester can be wrong") {
		t.Fatal("the note does not allow for a wrong check")
	}
}
