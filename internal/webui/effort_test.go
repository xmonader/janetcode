package webui

import (
	"encoding/json"
	"strings"
	"testing"
)

// The web UI parses and presents; the tier and the wording come from the
// same core command the terminal runs.
func TestWebEffortCommand(t *testing.T) {
	s, _ := planServer(t)
	if err := s.runCommand("/effort max"); err != nil {
		t.Fatal(err)
	}
	if got := s.opts.Agent.Settings().Effort; got != "max" {
		t.Fatalf("effort = %q", got)
	}
	if notes := strings.Join(s.noticeTexts(), "\n"); !strings.Contains(notes, "effort max") {
		t.Fatalf("notices = %q", notes)
	}
	if err := s.runCommand("/effort down"); err != nil {
		t.Fatal(err)
	}
	if got := s.opts.Agent.Settings().Effort; got != "xhigh" {
		t.Fatalf("after down, effort = %q", got)
	}
	if err := s.runCommand("/effort"); err != nil {
		t.Fatal(err)
	}
	if sheet := s.sheetText(t); !strings.Contains(sheet, "effort: xhigh") {
		t.Fatalf("sheet = %q", sheet)
	}
}

// The web UI's status payload carries the tier so the browser can show a
// chip beside the mode; the default tier carries nothing.
func TestWebStatusCarriesEffort(t *testing.T) {
	s, _ := planServer(t)
	st := s.statusNow()
	if st.Effort != "" {
		t.Fatalf("effort = %q, want the default to be silent", st.Effort)
	}
	if err := s.runCommand("/effort max"); err != nil {
		t.Fatal(err)
	}
	st = s.statusNow()
	if st.Effort != "max" {
		t.Fatalf("effort = %q", st.Effort)
	}
	if b, err := json.Marshal(st); err != nil || !strings.Contains(string(b), `"effort":"max"`) {
		t.Fatalf("payload = %s, %v", b, err)
	}
}
