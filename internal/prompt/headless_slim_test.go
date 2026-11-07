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

func TestHeadlessLiteralLine(t *testing.T) {
	const line = "do not rename, prefix with _ or tidy them."
	for name, p := range map[string]string{"Headless": Headless, "HeadlessSlim": HeadlessSlim} {
		if !strings.Contains(p, line) {
			t.Errorf("%s lacks the names line", name)
		}
	}
	for name, p := range map[string]string{"NoLiteral": HeadlessNoLiteral, "SlimNoLiteral": HeadlessSlimNoLiteral} {
		if strings.Contains(p, line) || p == Headless || p == HeadlessSlim {
			t.Errorf("%s still has the names line", name)
		}
	}
	if strings.Contains(HeadlessSlimNoLiteral, "then_run") {
		t.Error("slim variant names then_run")
	}
}
