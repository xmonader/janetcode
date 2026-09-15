package skill

import "testing"

func TestWithBuiltinsProjectWins(t *testing.T) {
	got := WithBuiltins([]Skill{{Name: "computer-use", Description: "project copy", Source: "project", Body: "x", UserInvocable: true, ModelInvocable: true}})
	found := false
	for _, s := range got {
		if s.Name == "computer-use" {
			found = true
			if s.Source != "project" || s.Description != "project copy" {
				t.Fatalf("project should win: %+v", s)
			}
		}
	}
	if !found {
		t.Fatal("computer-use missing")
	}
	if n := len(WithBuiltins(nil)); n != len(BuiltinSkills()) {
		t.Fatalf("builtins = %d", n)
	}
	if idx := IndexWithin(WithBuiltins(nil), 0); idx != "" {
		t.Fatalf("builtins must not sit in the skill index: %q", idx)
	}
}
