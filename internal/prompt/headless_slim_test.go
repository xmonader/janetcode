package prompt

import (
	"strings"
	"testing"
)

func TestHeadlessSlimDropsThenRun(t *testing.T) {
	if strings.Contains(HeadlessSlim, "then_run") {
		t.Fatal("the slim prompt still names then_run")
	}
	if HeadlessSlim == Headless || !strings.Contains(HeadlessSlim, "pytest path/to/test_x.py), not a large project's whole suite") {
		t.Fatalf("the slim prompt was not rewritten as intended:\n%s", HeadlessSlim)
	}
}

func TestHeadlessForLines(t *testing.T) {
	const names = "do not rename, prefix with _ or tidy them."
	const rules = "write the task's rules in your reply"
	for _, slim := range []bool{false, true} {
		full := HeadlessFor(slim, true, true)
		if !strings.Contains(full, names) || !strings.Contains(full, rules) {
			t.Errorf("slim=%v: the full prompt lacks a line", slim)
		}
		if p := HeadlessFor(slim, false, true); strings.Contains(p, names) || !strings.Contains(p, rules) {
			t.Errorf("slim=%v: literal off did not drop only the names line", slim)
		}
		if p := HeadlessFor(slim, true, false); strings.Contains(p, rules) || !strings.Contains(p, names) {
			t.Errorf("slim=%v: rulesfirst off did not drop only the rules line", slim)
		}
	}
	if strings.Contains(HeadlessFor(true, false, false), "then_run") {
		t.Error("slim variant names then_run")
	}
}
