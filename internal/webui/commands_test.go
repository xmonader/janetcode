package webui

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/slash"
)

// TestDispatchCoversWebCommands checks webCommands, the slash table and
// webDispatch against each other. webCommands is what the palette, /help
// and the commands API are all built from, so a command that only has a
// handler is invisible everywhere else, and one that is listed but has no
// handler answers "unknown command". It mirrors internal/tui's
// TestDispatchCoversTheTable for the terminal dispatcher.
func TestDispatchCoversWebCommands(t *testing.T) {
	known := map[string]bool{}
	for _, name := range webCommands {
		known[name] = true
		if _, ok := slash.Lookup(name); !ok {
			t.Errorf("%s is in webCommands but not in the slash table", name)
		}
		if webDispatch[name] == nil {
			t.Errorf("%s is in webCommands but runCommand never dispatches it", name)
		}
	}
	if len(commandSpecs) != len(webCommands) {
		t.Errorf("commandSpecs has %d entries for %d webCommands", len(commandSpecs), len(webCommands))
	}
	for name := range webDispatch {
		if known[name] {
			continue
		}
		if target := slash.Aliases[name]; target == "" || !known[target] {
			t.Errorf("%s is dispatched but is neither in webCommands nor an alias of one", name)
		}
	}
	for name := range webArgs {
		if !known[name] {
			t.Errorf("webArgs narrows %s, which is not in webCommands", name)
		}
	}
}

// TestAliasesObeyTheIdleGate: an alias of an idle-only command is
// refused mid-turn exactly as the canonical name is. The gate compared
// the typed name with commandSpecs, which carries canonical names only,
// so /new started a new session out from under a running turn while
// /clear was correctly refused.
func TestAliasesObeyTheIdleGate(t *testing.T) {
	if slash.Canonical("/new") != "/clear" {
		t.Fatal("/new should be an alias of /clear")
	}
	for _, name := range []string{"/clear", "/new"} {
		s := newServer(context.Background(), Options{})
		s.running = true
		err := s.runCommand(name)
		if err == nil || !strings.Contains(err.Error(), "waits for the turn to finish") {
			t.Errorf("%s during a running turn: err = %v, want a refusal", name, err)
		}
	}
}
