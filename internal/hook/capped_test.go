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

func TestMatchReIsCachedAndSafe(t *testing.T) {
	re, err := matchRe("^bash$")
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString("bash") || re.MatchString("read_file") {
		t.Fatal("a valid pattern matched wrongly")
	}
	// Twice: the second answer is the memoised one, the same compiled
	// pattern back.
	re2, err := matchRe("^bash$")
	if err != nil || re2 != re {
		t.Fatal("the cached pattern was not the one returned first")
	}
	// A pattern that does not compile is an error the runner logs, not a
	// hook that silently never runs.
	if _, err := matchRe("(["); err == nil {
		t.Fatal("a broken pattern must be reported")
	}
}
