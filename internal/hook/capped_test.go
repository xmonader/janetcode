package hook

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// What capped keeps is pasted into the conversation, so it stops on a
// rune boundary rather than leaving half a character behind.
func TestCappedStopsOnARuneBoundary(t *testing.T) {
	for max := 1; max <= 8; max++ {
		c := &capped{max: max}
		if _, err := c.Write([]byte(strings.Repeat("é", 8))); err != nil {
			t.Fatal(err)
		}
		got := c.String()
		kept := strings.SplitN(got, "\n[", 2)[0]
		if !utf8.ValidString(kept) {
			t.Fatalf("max=%d kept invalid UTF-8: %q", max, kept)
		}
		if len(kept) > max {
			t.Fatalf("max=%d kept %d bytes", max, len(kept))
		}
		if !strings.Contains(got, "were dropped") {
			t.Fatalf("max=%d did not report the drop: %q", max, got)
		}
	}
	// Everything that fits is kept, and nothing is reported.
	c := &capped{max: 64}
	c.Write([]byte("héllo"))
	if got := c.String(); got != "héllo" {
		t.Fatalf("String() = %q", got)
	}
}

func TestMatchesToolIsCachedAndSafe(t *testing.T) {
	if !matchesTool("^bash$", "bash") || matchesTool("^bash$", "read_file") {
		t.Fatal("a valid pattern matched wrongly")
	}
	// Twice, to go through the memo.
	if !matchesTool("^bash$", "bash") {
		t.Fatal("the cached pattern stopped matching")
	}
	// A pattern that does not compile matches nothing; Validate is what
	// reports it to whoever wrote it.
	if matchesTool("([", "bash") {
		t.Fatal("a broken pattern should match nothing")
	}
}
