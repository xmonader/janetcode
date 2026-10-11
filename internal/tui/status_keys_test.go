package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/keymap"
)

// Status lines named ctrl+s and esc whatever the keymap said: with steer
// unbound and cancel moved, "/clear waits for the turn" told the user to
// press keys that did nothing.
func TestStatusLinesNameTheBoundKeys(t *testing.T) {
	h := newHarness(t)
	km, err := keymap.Resolve(keymap.Overrides{"steer": {}, "cancel": {"f4"}})
	if err != nil {
		t.Fatal(err)
	}
	h.m.keys = km
	got := h.m.waitsForTurn("/clear")
	if strings.Contains(got, "ctrl+s") || strings.Contains(got, "esc") || !strings.Contains(got, "f4 cancels it") {
		t.Fatalf("waitsForTurn = %q", got)
	}
	if def := newHarness(t).m.waitsForTurn("/clear"); def != "/clear waits for the turn to finish (esc cancels it, ctrl+s steers it)" {
		t.Fatalf("default keymap changed the text: %q", def)
	}
}

// The command menu showed /effort's "alt+e cycles it" whatever the keymap
// said; it now shows the bound key, as /help does.
func TestCommandMenuNamesTheBoundKey(t *testing.T) {
	h := newHarness(t)
	km, err := keymap.Resolve(keymap.Overrides{"effort": {"f6"}})
	if err != nil {
		t.Fatal(err)
	}
	h.m.keys = km
	c, _ := lookup("/effort")
	got := h.m.boundKeysIn(c.name, c.help+c.keyHelp)
	if strings.Contains(got, "alt+e") || !strings.Contains(got, "f6 cycles it") {
		t.Fatalf("menu help = %q", got)
	}
}
