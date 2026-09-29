package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/xmonader/janetcode/internal/i18n"
)

// With the interface in Spanish, /help, the permission prompt's keys and
// the mode hint are Spanish, and the permission line still fits the width
// it is given: translations run longer than the English they replace.
func TestInterfaceSpeaksTheCatalogueLanguage(t *testing.T) {
	i18n.SetLanguage("es")
	defer i18n.SetLanguage("en")
	h := newHarness(t)

	help := h.m.briefHelpText()
	for _, want := range []string{"teclas\n", "\ncomandos\n", "cambiar de modelo", "todas las teclas y los"} {
		if !strings.Contains(help, want) {
			t.Errorf("brief help lacks %q:\n%s", want, help)
		}
	}
	// Command names are not translated: they are what is typed.
	if !strings.Contains(help, "/model") {
		t.Errorf("a command name was translated:\n%s", help)
	}
	full := h.m.helpText()
	for _, want := range []string{"día a día", "revisar el trabajo", "permisos: y permitir una vez"} {
		if !strings.Contains(full, want) {
			t.Errorf("/help all lacks %q", want)
		}
	}
	// The everyday group's descriptions are catalogued; the reference
	// text of the rest stays English. Rendered at a width, so compare
	// with the whitespace folded.
	folded := strings.Join(strings.Fields(full), " ")
	if !strings.Contains(folded, "volver a aplicar lo que deshizo el último /undo") {
		t.Errorf("/help all's /redo description is not Spanish:\n%s", full)
	}
	if !strings.Contains(folded, "fan out two subagents over the current diff") {
		t.Errorf("/review's description should stay English:\n%s", full)
	}

	for _, w := range []int{60, 80, 200} {
		h.m.width = w
		line := plain(h.m.permissionKeys("bash", "bash(go test *)", "", false))
		if lipgloss.Width(line) > w-6 {
			t.Fatalf("width %d: %d cols: %q", w, lipgloss.Width(line), line)
		}
		if w == 200 && !strings.Contains(line, "permitir una vez") {
			t.Errorf("wide permission line is not Spanish: %q", line)
		}
		if !strings.Contains(line, "denegar") {
			t.Errorf("width %d: no Spanish deny: %q", w, line)
		}
	}
	if got := h.m.statusModeHint(); got != "las ediciones y los comandos preguntan primero" {
		t.Errorf("mode hint = %q", got)
	}
}
