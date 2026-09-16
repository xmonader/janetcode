package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Everything these functions cut goes on to be sent to the model: the
// goal and the verbatim user asks in the working-memory record, and a
// long script's output. A cut that lands inside a multi-byte rune turns
// into a replacement character in the request.

func TestPrefixNeverSplitsARune(t *testing.T) {
	// "é" is two bytes, so every odd budget lands mid-rune.
	s := strings.Repeat("é", 200)
	for n := 1; n < 40; n++ {
		got := prefix(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("prefix(%d bytes) is not valid UTF-8: %q", n, got)
		}
		if len(got) > n {
			t.Fatalf("prefix(%d) returned %d bytes", n, len(got))
		}
	}
	if got := prefix("ascii", 99); got != "ascii" {
		t.Fatalf("prefix under budget = %q", got)
	}
}

func TestScriptOutputIsCutOnRuneBoundaries(t *testing.T) {
	s := &Script{}
	// A single rune repeated: with a three-byte rune neither end of the
	// gap falls on a boundary by accident.
	out := strings.Repeat("→", scriptMaxOutput)
	got := s.report(out, "", nil, 3, nil)
	if !utf8.ValidString(got) {
		t.Fatal("the script report is not valid UTF-8")
	}
	if !strings.Contains(got, "of output dropped") {
		t.Fatalf("the output was not truncated: %d bytes", len(got))
	}
}
