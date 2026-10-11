package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/i18n"
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
	got := h.m.commandDesc(c, false)
	if strings.Contains(got, "alt+e") || !strings.Contains(got, "f6 cycles it") {
		t.Fatalf("menu help = %q", got)
	}
}

// In French, /help named keys the way the config writes them ("enter",
// "esc") once it was built from the keymap; it names them in French
// again. A translated description whose action has no key used to read
// "unbound" in place of the key; it now leaves the clause out.
func TestTranslatedHelpKeys(t *testing.T) {
	i18n.SetLanguage("fr")
	t.Cleanup(func() { i18n.SetLanguage("en") })
	h := newHarness(t)
	brief := h.m.briefHelpText()
	for _, want := range []string{"entrée", "échap", "maj+tab"} {
		if !strings.Contains(brief, want) {
			t.Errorf("French /help lacks %q:\n%s", want, brief)
		}
	}
	km, err := keymap.Resolve(keymap.Overrides{"effort": {}})
	if err != nil {
		t.Fatal(err)
	}
	h.m.keys = km
	c, _ := lookup("/effort")
	got := h.m.commandDesc(c, true)
	if strings.Contains(got, "unbound") || strings.Contains(got, "alt+e") || got != i18n.T(c.help) {
		t.Fatalf("translated /effort with no key = %q", got)
	}
}
