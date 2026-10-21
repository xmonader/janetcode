package localize

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/change"
)

// What the model is shown is cut on a rune boundary: a long changed line
// of non-ASCII text, and a report cut short, used to end in half a
// character.
func TestClippedTextStaysValidUTF8(t *testing.T) {
	r := Report{
		Edits: []Edit{{Path: "a.go"}},
		Units: []Unit{{Edit: 0, Hunk: change.Hunk{NewStart: 1, Lines: []string{"+" + strings.Repeat("é", 100)}}}},
	}
	if s := r.hunkText(0); !utf8.ValidString(s) {
		t.Fatalf("hunk text is not valid UTF-8: %q", s)
	}
	if s := cutLines(strings.Repeat("é", 10), 7); !utf8.ValidString(s) {
		t.Fatalf("cutLines left half a character: %q", s)
	}
}
