package recipe

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFormatStepsShowsOldNewCompactly checks /recipe show's rendering of a
// text-form edit_file step: it must read as "path: "old" → "new"" rather
// than the raw compacted JSON, which for a multi-line old_string/new_string
// is unreadable at a glance — exactly what a save-time conversion from a
// positional edit produces (see internal/agent's RecipeSteps).
func TestFormatStepsShowsOldNewCompactly(t *testing.T) {
	input, _ := json.Marshal(map[string]string{
		"path":       "CHANGELOG.md",
		"old_string": "## 1.0.0\n",
		"new_string": "## 1.1.0\n- bump version\n\n## 1.0.0\n",
	})
	steps := []Step{{Tool: "edit_file", Input: input}}
	got := FormatSteps(steps)
	if !strings.Contains(got, "CHANGELOG.md:") {
		t.Fatalf("got %q, want the path named", got)
	}
	if !strings.Contains(got, "→") {
		t.Fatalf("got %q, want an old → new arrow, not raw JSON", got)
	}
	if strings.Contains(got, `"old_string"`) {
		t.Fatalf("got %q, want the compact form, not the raw JSON field names", got)
	}
}

// TestFormatStepsNamesExpectPrecondition checks that a step with an Expect
// precondition (see Step.Expect) is called out in the one-line summary, so
// /recipe show tells a reader what replay will refuse to bypass.
func TestFormatStepsNamesExpectPrecondition(t *testing.T) {
	input, _ := json.Marshal(map[string]string{"path": "new.txt", "content": "hello\n"})
	steps := []Step{{Tool: "write_file", Input: input, Expect: &Expect{Absent: true}}}
	got := FormatSteps(steps)
	if !strings.Contains(got, "must not already exist") {
		t.Fatalf("got %q, want the precondition named", got)
	}
}

// TestFormatStepsFallsBackToCompactJSONForOtherTools checks that a bash
// step (or any non-edit_file step) still renders as compact JSON, unaffected
// by the old→new special case.
func TestFormatStepsFallsBackToCompactJSONForOtherTools(t *testing.T) {
	steps := []Step{{Tool: "bash", Input: json.RawMessage(`{"command":"echo hi"}`)}}
	got := FormatSteps(steps)
	if !strings.Contains(got, `"command":"echo hi"`) {
		t.Fatalf("got %q, want the compact JSON form", got)
	}
}
