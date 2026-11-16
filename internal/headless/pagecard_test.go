package headless

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
)

// A render_html result in a -p run prints the one-line card with the
// whole path, as the manual says, and not the 300-character clip of the
// result sentence (a deep session directory pushes the title off the end).
func TestRenderHTMLResultPrintsTheCardInHeadlessRuns(t *testing.T) {
	path := "/home/u/.local/share/janetcode/sessions/" + strings.Repeat("d", 200) + "/20261116-135714-e3d24c.page-1.html"
	out := "Page saved at " + path + " (page 1), titled \"Quarterly Sales\". It is shown above your reply in the web UI."
	var errb strings.Builder
	printEvent(Options{Stderr: &errb, Stdout: &strings.Builder{}}, agent.Event{
		Kind: agent.EvToolResult, Call: &agent.ToolCall{Name: "render_html"}, Output: out,
	})
	want := "▣ Quarterly Sales · " + path
	if got := strings.TrimSpace(errb.String()); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
