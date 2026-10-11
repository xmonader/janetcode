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
