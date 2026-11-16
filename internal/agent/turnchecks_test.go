package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/session"
)

// A check result is tied to its turn and to the calls whose changes it
// followed, and /changes prints both; a check from another turn is not
// credited with this turn's calls.
func TestTurnChecksLinkChecksToTurnsAndCalls(t *testing.T) {
	log := &session.Memory{}
	for _, e := range []session.Event{
		{Type: session.TypeFileChange, Turn: 1, CallID: "c1", Name: "/w/a.go", Arguments: "absent->abc"},
		{Type: session.TypeFileChange, Turn: 1, CallID: "c2", Name: "/w/b.go", Arguments: "x->y"},
		{Type: session.TypeFileChange, Turn: 1, CallID: "c2", Name: "/w/c.go", Arguments: "x->y"},
		{Type: session.TypeGate, Turn: 1, Name: "go test ./...", Source: session.GateVerify},
		{Type: session.TypeFileChange, Turn: 2, CallID: "c9", Name: "/w/a.go", Arguments: "abc->def"},
		{Type: session.TypeGate, Turn: 2, Name: "go vet ./...", IsError: true, Source: session.GateVerify},
	} {
		if _, err := log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	a := &Agent{Log: log}
	checks := a.TurnChecks()
	if len(checks) != 2 {
		t.Fatalf("checks = %+v", checks)
	}
	if c := checks[0]; c.Turn != 1 || c.Failed || strings.Join(c.Calls, ",") != "c1,c2" {
		t.Errorf("turn 1 check = %+v, want passed after c1,c2 once each", c)
	}
	if c := checks[1]; c.Turn != 2 || !c.Failed || strings.Join(c.Calls, ",") != "c9" {
		t.Errorf("turn 2 check = %+v, want failed after c9 only", c)
	}
	text := FormatTurnChecks(checks)
	for _, want := range []string{"turn 1 check go test ./... passed  (after call c1, c2)", "turn 2 check go vet ./... FAILED  (after call c9)"} {
		if !strings.Contains(text, want) {
			t.Errorf("FormatTurnChecks lacks %q:\n%s", want, text)
		}
	}
}

// Each changed file names the turn and the tool call that wrote it.
func TestChangeFormatsNameTurnAndCall(t *testing.T) {
	c := change.Change{Seq: 3, Turn: 2, Call: "call_77", Tool: "edit_file", Path: "/w/a.go", Existed: true, Before: []byte("a\n"), After: []byte("b\n")}
	if got := FormatChange(c, "/w"); !strings.Contains(got, "from turn 2, tool call call_77") {
		t.Errorf("FormatChange:\n%s", got)
	}
	groups := []ChangeGroup{{Turn: 2, Changes: []Change{c}}}
	if got := FormatChangeGroups(groups, "/w", nil); !strings.Contains(got, "#3   edit_file  a.go  +1 -1  call call_77") {
		t.Errorf("FormatChangeGroups:\n%s", got)
	}
}
