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
