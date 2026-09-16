package feature

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The introspect budget is a byte count and the rows it trims carry em
// dashes, so a cut can land inside one. What the model gets back must
// still be valid UTF-8.
func TestAnswerStaysValidUTF8AtTheBudget(t *testing.T) {
	c := New()
	// One feature whose description is a long run of multi-byte
	// characters with no line break to cut at.
	c.Add(Feature{
		Kind: KindBehave, Name: "budget",
		What: strings.Repeat("— budget ", MaxAnswerBytes),
		When: "always",
	})
	out := Answer(c, "budget", "")
	if !utf8.ValidString(out) {
		t.Fatalf("the truncated answer is not valid UTF-8: %q", out[max(0, len(out)-40):])
	}
	if !strings.Contains(out, "truncated") {
		t.Fatal("the answer was not truncated; the test is not exercising the cut")
	}
}

func TestDropPartialRune(t *testing.T) {
	full := "ab—"
	for cut := 1; cut <= len(full); cut++ {
		got := dropPartialRune(full[:cut])
		if !utf8.ValidString(got) {
			t.Errorf("dropPartialRune(%q) = %q, not valid UTF-8", full[:cut], got)
		}
	}
	if got := dropPartialRune("plain"); got != "plain" {
		t.Errorf("dropPartialRune stripped a whole string: %q", got)
	}
	if got := dropPartialRune(""); got != "" {
		t.Errorf("dropPartialRune(\"\") = %q", got)
	}
	// A genuine replacement character the caller wrote is three bytes and
	// must survive.
	if got := dropPartialRune("a�"); got != "a�" {
		t.Errorf("a whole U+FFFD was stripped: %q", got)
	}
}
