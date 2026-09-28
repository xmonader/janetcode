package tool

import (
	"strings"
	"testing"
)

// Adjacent table cells are separate values: a version table that reads
// "go1.22" instead of "go 1.22" says something the page does not.
func TestHTMLToTextKeepsTableCellsApart(t *testing.T) {
	got := htmlToText(`<table><tr><th>Name</th><th>Version</th></tr><tr><td>go</td><td>1.22</td></tr></table>`)
	for _, want := range []string{"Name Version", "go 1.22"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
