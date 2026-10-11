package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/keymap"
)

// /help named the default keys in plain text, so with the keybindings
// moved it told you to press keys that do nothing. It now names the key
// each action has, and leaves out what an unbound action would do.
func TestHelpNamesTheBoundKeys(t *testing.T) {
	h := newHarness(t)
	km, err := keymap.Resolve(keymap.Overrides{
		"background":      {"f6"},
		"steer":           {},
		"expand":          {"f7"},
		"mode_cycle":      {"f8"},
		"newline":         {"alt+j"},
		"interrupt":       {"ctrl+q"},
		"quit":            {"f9"},
		"redraw":          {"f10"},
		"paste_image":     {},
		"external_editor": {"f11"},
		"page_up":         {"f2"},
		"page_down":       {"f3"},
		"effort":          {},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.m.keys = km

	full, brief := h.m.helpText(), h.m.briefHelpText()
	for name, text := range map[string]string{"/help all": full, "/help": brief} {
		for _, stale := range []string{"ctrl+g", "ctrl+s", "ctrl+o", "shift+tab", "ctrl+j", "alt+enter", "ctrl+c", "ctrl+d", "ctrl+l", "ctrl+v", "ctrl+x", "pgup", "pgdn", "alt+e", "unbound"} {
			if strings.Contains(text, stale) {
				t.Errorf("%s names %q, which this keymap does not have:\n%s", name, stale, text)
			}
		}
	}
	for _, want := range []string{"f6 moves a command", "f7 expand", "f8 cycles ask", "alt+j", "ctrl+q cancel", "f9 quit", "f10 redraw", "f11 writes the prompt", "f2/f3"} {
		if !strings.Contains(full, want) {
			t.Errorf("/help all lacks %q:\n%s", want, full)
		}
	}
	if folded := strings.Join(strings.Fields(full), " "); !strings.Contains(folded, "(f7 expands one turn)") {
		t.Errorf("/verbose's description does not name the bound key:\n%s", full)
	}
	for _, want := range []string{"alt+j newline", "f9 quit", "f8 changes what asks first"} {
		if !strings.Contains(brief, want) {
			t.Errorf("/help lacks %q:\n%s", want, brief)
		}
	}

	// The defaults read as they always did.
	h.m.keys = keymap.Default()
	full, brief = h.m.helpText(), h.m.briefHelpText()
	for _, want := range []string{"ctrl+g moves a command", "ctrl+s hands it", "ctrl+o expand", "ctrl+j / alt+enter newline"} {
		if !strings.Contains(full, want) {
			t.Errorf("default /help all lacks %q:\n%s", want, full)
		}
	}
	if !strings.Contains(brief, "enter send · ctrl+j newline · esc cancel the turn · ctrl+d quit") {
		t.Errorf("default /help changed:\n%s", brief)
	}
}
