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

// TestPermissionKeyLineMarksTheKeys: the prompt's key line was the input
// box's faint placeholder, so y, a, n and c read as part of the prose.
// Each key is now bold in the warning colour, and only the keys: an "a"
// or a "c" inside a word stays faint.
func TestPermissionKeyLineMarksTheKeys(t *testing.T) {
	saved := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(saved)

	sty := defaultStyles()
	key := sty.warn.Bold(true)
	for _, line := range []string{
		"y allow once   a always for bash this session   n deny (and say why)   c cancel the turn",
		"y once · a all write_file this session · r save write_file(./*) ←→ 2/3 · n deny · c cancel",
		"y  a session  r save write_file  n deny  c cancel",
	} {
		out := keyLine(sty, line)
		if ansi.Strip(out) != line {
			t.Fatalf("the text changed: %q", ansi.Strip(out))
		}
		if n := strings.Count(out, key.Render("y")) + strings.Count(out, key.Render("a")) + strings.Count(out, key.Render("n")) + strings.Count(out, key.Render("c")); n != 4 {
			t.Errorf("%d of the keys y, a, n, c marked in %q", n, out)
		}
		if strings.Contains(line, "r save") && !strings.Contains(out, key.Render("r")) {
			t.Errorf("r not marked in %q", out)
		}
	}
}

// TestPlaceholderFollowsTheTheme: the input box's placeholder kept
// textarea's fixed grey, near-black on a light terminal. It is the
// theme's faint colour, and follows a /theme switch.
func TestPlaceholderFollowsTheTheme(t *testing.T) {
	h := newHarness(t)
	if got, want := h.m.input.FocusedStyle.Placeholder.GetForeground(), h.m.styles.faint.GetForeground(); got != want {
		t.Fatalf("placeholder %v, faint %v", got, want)
	}
	h.m.applyThemeChoice("classic", false)
	if got, want := h.m.input.FocusedStyle.Placeholder.GetForeground(), h.m.styles.faint.GetForeground(); got != want {
		t.Fatalf("after /theme: placeholder %v, faint %v", got, want)
	}
}
