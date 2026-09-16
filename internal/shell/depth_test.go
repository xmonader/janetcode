package shell

import (
	"strings"
	"testing"
)

// nest wraps command in n layers of `sh -c`.
func nest(command string, n int) string {
	for i := 0; i < n; i++ {
		command = "sh -c " + quote(command)
	}
	return command
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// The classifier is a safety gate, so running out of patience has to
// mean "ask", not "fine". Before, every wrapper started the depth count
// again, so a deep enough nest was graded ordinary — and a nest is not a
// shape an honest command line takes.
func TestDeepNestingIsUnknownRatherThanOrdinary(t *testing.T) {
	root := "/work/repo"
	// Shallow enough to see through: the verdict is about what it runs.
	if risk, _ := Classify(nest("rm -rf /", 2), root); risk != RiskCatastrophic {
		t.Fatalf("a two-deep nest of rm -rf / graded %v", risk)
	}
	// Too deep to see through: not ordinary.
	deep := nest("echo hi", MaxWrapperDepth+2)
	risk, why := Classify(deep, root)
	if risk != RiskUnknown {
		t.Fatalf("a %d-deep nest graded %v (%s), want RiskUnknown", MaxWrapperDepth+2, risk, why)
	}
	if why == "" {
		t.Fatal("RiskUnknown came back with no reason")
	}
	// And whatever hides at the bottom of it is never called safe.
	if SafeRead(nest("rm -rf /", MaxWrapperDepth+2)) {
		t.Fatal("a nest too deep to read was called a safe read")
	}
	if SafeRead(deep) {
		t.Fatal("a nest too deep to read was called a safe read")
	}
	// An eval nest resets nothing either.
	if risk, _ := Classify(strings.Repeat("eval ", MaxWrapperDepth+2)+"echo hi", root); risk != RiskUnknown {
		t.Fatalf("a deep eval nest graded %v", risk)
	}
}

// RiskUnknown sits above ordinary and below the two verdicts that mean
// something concrete, so callers that ask about anything worse than
// ordinary keep working and one that asks for the catastrophic case
// alone is unaffected.
func TestRiskOrdering(t *testing.T) {
	if !(RiskOrdinary < RiskUnknown && RiskUnknown < RiskEscapes && RiskEscapes < RiskCatastrophic) {
		t.Fatal("risk levels are out of order")
	}
	for _, r := range []Risk{RiskOrdinary, RiskUnknown, RiskEscapes, RiskCatastrophic} {
		if r.String() == "" {
			t.Errorf("risk %d has no name", r)
		}
	}
}
