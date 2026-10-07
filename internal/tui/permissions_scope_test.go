package tui

import (
	"strings"
	"testing"
)

// /permissions ask adds a sensitive-call rule, the list shows it, and
// /permissions check says which rule decides a call and what it outranked.
func TestPermissionsAskAndCheck(t *testing.T) {
	h := newHarness(t)
	ag := h.m.opts.Agent
	h.m.permissionsCommand([]string{"allow", "bash(*)"})
	h.m.permissionsCommand([]string{"ask", "*(touches:**/.env)"})
	h.m.permissionsCommand([]string{"deny", "bash(cmd:rm", "*)"})
	rs := ag.RulesSnapshot()
	if len(rs.Ask) != 1 || rs.Ask[0].String() != "*(touches:**/.env)" {
		t.Fatalf("ask rules = %+v", rs.Ask)
	}
	h.m.permissionsCommand(nil)
	h.m.permissionsCommand([]string{"check", "cat", ".env"})
	h.m.permissionsCommand([]string{"check", "rm", "-rf", "build"})
	h.m.permissionsCommand([]string{"check", "read_file", `{"path":"svc/.env"}`})
	var body strings.Builder
	for _, e := range h.m.transcript.entries {
		body.WriteString(e.text + "\n")
	}
	for _, want := range []string{
		"ask    *(touches:**/.env)",
		"always asks because of rule *(touches:**/.env), which outranks allow rule bash(*)",
		"denied by rule bash(cmd:rm *), which outranks allow rule bash(*)",
		"read_file",
	} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("missing %q in:\n%s", want, body.String())
		}
	}
}
