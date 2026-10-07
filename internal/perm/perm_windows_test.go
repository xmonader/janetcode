//go:build windows

package perm

import (
	"encoding/json"
	"strings"
	"testing"
)

// On Windows the subject was docs\a.md while the rungs offered were
// docs/*, so a directory rule saved from a prompt never matched the call
// it was saved for.
func TestWindowsPathRulesMatchTheCallTheyWereSavedFor(t *testing.T) {
	in, _ := json.Marshal(map[string]string{"path": `docs\guide\a.md`, "content": "x"})
	subject := Subject("write_file", in)
	if subject != "docs/guide/a.md" {
		t.Fatalf("subject = %q, want slash-separated", subject)
	}
	rungs := SuggestLadder("write_file", subject)
	want := []string{"docs/guide/a.md", "docs/guide/*", "write:docs/guide/*", "docs/*"}
	if len(rungs) != len(want) {
		t.Fatalf("rungs = %v, want %v", rungs, want)
	}
	for i, r := range rungs {
		if r.Pattern != want[i] {
			t.Errorf("rung %d = %q, want %q", i, r.Pattern, want[i])
		}
		if !strings.Contains(r.Pattern, ":") && !Match(r.Pattern, subject) {
			t.Errorf("rung %q does not match the call it came from (%q)", r.Pattern, subject)
		}
	}
	// A path at the top of a drive widens to nothing but the tool.
	top := SuggestLadder("write_file", Subject("write_file", json.RawMessage(`{"path":"C:\\a.md"}`)))
	for _, r := range top {
		if r.Pattern == "C:/*" || r.Pattern == "C:*" {
			t.Errorf("a rung widens to the whole drive: %v", top)
		}
	}
}
