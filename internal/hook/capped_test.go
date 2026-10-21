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

// A write that exactly fills the room left must not index past its own
// end looking for a rune boundary: os/exec copies a hook's output in
// 32 KiB chunks, so two full chunks against the 64 KiB default did this,
// and the panic in the copying goroutine took the whole process down.
func TestCappedExactFit(t *testing.T) {
	c := &capped{max: 8}
	if _, err := c.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte("efgh")); err != nil {
		t.Fatal(err)
	}
	if got := c.String(); got != "abcdefgh" {
		t.Fatalf("String() = %q", got)
	}
	c.Write([]byte("i"))
	if got := c.String(); !strings.HasPrefix(got, "abcdefgh\n[1 more bytes") {
		t.Fatalf("String() = %q", got)
	}
}

// Stopping short on a rune boundary leaves a few bytes of room. A later
// write must not fill them: the kept text would then be the start of the
// output glued to a piece from further on, with the bytes between gone
// and nothing saying where.
func TestCappedKeepsAContiguousPrefix(t *testing.T) {
	c := &capped{max: 4}
	c.Write([]byte("abcé")) // 5 bytes: é does not fit, 1 byte of room is left
	c.Write([]byte("XYZ"))
	kept := strings.SplitN(c.String(), "\n[", 2)[0]
	if kept != "abc" {
		t.Fatalf("kept %q, want the contiguous prefix %q", kept, "abc")
	}
	if !strings.Contains(c.String(), "[5 more bytes") {
		t.Fatalf("String() = %q", c.String())
	}
}
