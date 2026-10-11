package slash

import (
	"strings"
	"testing"
)

// TestIdleFlags checks that Commands carries Idle exactly for the
// commands idleOnly names, from the moment the package is initialised.
func TestIdleFlags(t *testing.T) {
	idle := map[string]bool{}
	for _, n := range idleOnly {
		idle[n] = true
	}
	seen := 0
	for _, c := range Commands {
		if c.Idle != idle[c.Name] {
			t.Errorf("%s: Idle = %v, want %v", c.Name, c.Idle, idle[c.Name])
		}
		if c.Idle {
			seen++
		}
	}
	if seen == 0 {
		t.Fatal("no command is idle-only")
	}
	if c, ok := Lookup("/compact"); !ok || !c.Idle {
		t.Errorf("/compact must wait for the turn: %+v", c)
	}
	if c, ok := Lookup("/help"); !ok || c.Idle {
		t.Errorf("/help must run mid-turn: %+v", c)
	}
}

// The shared descriptions are also the web UI's, which has none of the
// terminal's keys: a key the terminal binds is named only in KeyHelp.
func TestSharedHelpNamesNoTerminalKey(t *testing.T) {
	for _, c := range Commands {
		for _, k := range []string{"alt+e", "ctrl+o", "esc esc", "; tab accepts"} {
			if strings.Contains(c.Help, k) {
				t.Errorf("%s help names %q; put that clause in KeyHelp", c.Name, k)
			}
		}
	}
}
