package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestCodeBlockIsCutNotWordWrapped: glamour word-wraps everything in a
// document at spaces and punctuation, code included, so at forty columns
// `fmt.Println("…")` came out as `fmt.` on one line and `Println(` on the
// next, the continuation back at the document's margin. A code line wider
// than the window is cut at the width and continued under its own indent;
// a code line that fits is left whole; prose still wraps at words.
func TestCodeBlockIsCutNotWordWrapped(t *testing.T) {
	const width = 40
	for _, intro := range []string{"Here:\n\n", "Here:\n"} {
		tr := newTranscript()
		assistantSaid(tr, intro+"```go\nfunc main() {\n\tfmt.Println(\"Hello, world from a slightly longer string\")\n\tx := a.b(c)\n}\n```\n\nDone with some prose that is long enough to wrap around the forty column width.")
		out := plain(tr.render(renderOpts{width: width, renderer: builtinRenderers}))
		lines := strings.Split(out, "\n")
		at := -1
		for i, l := range lines {
			if lipgloss.Width(l) > width-2 && strings.TrimSpace(l) != "" {
				// The right margin glamour keeps for prose is kept for code.
				if lipgloss.Width(strings.TrimRight(l, " ")) > width-2 {
					t.Errorf("line %d is wider than the text column: %q", i, l)
				}
			}
			if strings.Contains(l, "fmt.") {
				at = i
			}
		}
		if at < 0 || at+1 >= len(lines) {
			t.Fatalf("the Println line is missing:\n%s", out)
		}
		first := strings.TrimRight(lines[at], " ")
		if !strings.Contains(first, `fmt.Println("Hello`) {
			t.Errorf("the code line was broken at punctuation, not at the width: %q\n%s", first, out)
		}
		if lipgloss.Width(first) != width-2 {
			t.Errorf("an over-wide code line should be cut at the width (%d), got %d: %q\n%s", width-2, lipgloss.Width(first), first, out)
		}
		cont := lines[at+1]
		ind := len(first) - len(strings.TrimLeft(first, " "))
		if !strings.HasPrefix(cont, strings.Repeat(" ", ind)) {
			t.Fatalf("the continuation should keep the code line's indent %d: %q\n%s", ind, cont, out)
		}
		// A hard cut: nothing is dropped or added at the break.
		joined := strings.TrimSpace(first) + strings.TrimRight(cont[ind:], " ")
		if !strings.Contains(joined, `fmt.Println("Hello, world from a slightly longer string")`) {
			t.Errorf("the cut line and its continuation should read as the source line: %q\n%s", joined, out)
		}
		if !strings.Contains(out, "x := a.b(c)") {
			t.Errorf("a code line that fits should stay whole:\n%s", out)
		}
		if !strings.Contains(out, "Done with some prose that is long\n") && !strings.Contains(out, "Done with some prose that is long \n") {
			t.Errorf("prose should still wrap at words at the width:\n%s", out)
		}
	}
}
