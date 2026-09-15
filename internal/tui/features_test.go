package tui

import (
	"testing"

	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/slash"
)

// TestDispatchCoversTheTable checks the dispatch table against the slash
// table in both directions: every command and every alias has a handler,
// and every handler answers to a command or a declared alias. The table
// is what /help, tab completion and the catalogue are all built from, so
// a command that only has a handler is invisible everywhere else.
func TestDispatchCoversTheTable(t *testing.T) {
	known := map[string]bool{}
	for _, c := range commands {
		known[c.name] = true
		if dispatch[c.name] == nil {
			t.Errorf("%s is in the slash table but the terminal never dispatches it", c.name)
		}
	}
	if len(commands) != len(slash.Commands) {
		t.Errorf("the terminal lists %d commands, the slash table %d", len(commands), len(slash.Commands))
	}
	for alias, target := range slash.Aliases {
		if dispatch[alias] == nil {
			t.Errorf("alias %s is declared but the terminal never dispatches it", alias)
		}
		if !known[target] {
			t.Errorf("alias %s names %s, which is not a command", alias, target)
		}
	}
	for name := range dispatch {
		if !known[name] && slash.Aliases[name] == "" {
			t.Errorf("%s is dispatched but is neither in the command table nor a declared alias", name)
		}
	}
}

// TestHelpTopicFindsTheObviousThings is the behaviour /help <topic> is
// for: a user who knows the problem but not the name.
func TestHelpTopicFindsTheObviousThings(t *testing.T) {
	cat := feature.New()
	cat.AddAll(Features())
	for _, tc := range []struct{ query, want string }{
		{"undo", "/undo"},
		{"remember things across sessions", "/remember"},
		{"tests after every change", "/verify"},
		{"context is full", "/compact"},
		{"command is taking forever", "ctrl+g"},
		{"rsm", "/resume"},
	} {
		hits := cat.Find(tc.query, 5)
		ok := false
		for _, h := range hits {
			if h.Name == tc.want {
				ok = true
			}
		}
		if !ok {
			var got []string
			for _, h := range hits {
				got = append(got, h.Name)
			}
			t.Errorf("%q did not find %s; got %v", tc.query, tc.want, got)
		}
	}
}
