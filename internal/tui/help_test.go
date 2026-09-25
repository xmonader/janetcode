package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// TestHelpSheetScans: /help drew every line in one dim colour, so a
// command's name did not stand out from what it does. The sheet now
// sets headings in bold and names in the accent, and its text is
// unchanged underneath, so NO_COLOR and --plain read the same.
func TestHelpSheetScans(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)

	sty := defaultStyles()
	text := "keys\n  enter send · esc cancel the turn\n\ncommands\n  /connect   pick a provider and paste a key\n\n/help all        every key"
	out := helpSheet(sty, text, 80)
	for _, want := range []string{
		sty.heading.Render("keys"),
		sty.heading.Render("commands"),
		sty.key.Render("/connect"),
		sty.key.Render("/help"),
		sty.dim.Render("   pick a provider and paste a key"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%q", want, out)
		}
	}
	var plainLines []string
	for _, l := range strings.Split(ansi.Strip(out), "\n") {
		plainLines = append(plainLines, strings.TrimRight(l, " "))
	}
	if got := strings.Join(plainLines, "\n"); got != text {
		t.Errorf("the text changed under the colour:\n%s", got)
	}
}
