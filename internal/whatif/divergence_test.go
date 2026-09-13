package whatif

import "testing"

// TestDivergenceIgnoresArgumentPhrasing is the end-to-end finding: the
// same edit written as a line range in one run and as an edits list in
// the other was reported as the divergence, hiding the test run the
// changed prompt actually skipped.
func TestDivergenceIgnoresArgumentPhrasing(t *testing.T) {
	orig := []CallInfo{
		{Turn: 2, Name: "edit_file", Target: callTarget("edit_file", `{"path":"/repo/add.go","from_line":3,"to_line":3,"new_string":"a+b"}`, "/repo")},
		{Turn: 2, Name: "bash", Target: callTarget("bash", `{"command":"go test ./..."}`, "/repo")},
	}
	now := []CallInfo{
		{Turn: 2, Name: "edit_file", Target: callTarget("edit_file", `{"path":"/tmp/wt/add.go","edits":[{"old":"a-b","new":"a+b"}]}`, "/tmp/wt"), Arguments: "different"},
	}
	d, argDiffs := firstDivergence(orig, now)
	if d == nil || d.Index != 1 || d.Original == nil || d.Original.Name != "bash" || d.Now != nil {
		t.Fatalf("divergence = %+v, want the missing test run at call #2", d)
	}
	if argDiffs != 1 {
		t.Fatalf("argDiffs = %d, want 1 (the rephrased edit)", argDiffs)
	}
}

func TestCallTargetRelativisesRoot(t *testing.T) {
	if got := callTarget("read_file", `{"path":"/tmp/wt/pkg/a.go"}`, "/tmp/wt"); got != "pkg/a.go" {
		t.Fatalf("target = %q", got)
	}
	if got := callTarget("bash", `{"command":"go  test   /tmp/wt/pkg"}`, "/tmp/wt"); got != "go test pkg" {
		t.Fatalf("bash target = %q", got)
	}
}
