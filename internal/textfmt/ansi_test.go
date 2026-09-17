package textfmt

import (
	"strings"
	"testing"
)

func TestStripUnsafeANSIDropsOSC(t *testing.T) {
	// OSC 52 clipboard theft, BEL-terminated.
	got := StripUnsafeANSI("before\x1b]52;c;aGVsbG8=\x07after")
	if got != "beforeafter" {
		t.Fatalf("got %q", got)
	}
	// OSC 8 hyperlink, ST-terminated.
	got = StripUnsafeANSI("\x1b]8;;http://evil.example\x1b\\link\x1b]8;;\x1b\\")
	if got != "link" {
		t.Fatalf("got %q", got)
	}
	// Window-title change.
	got = StripUnsafeANSI("\x1b]0;pwned\x07tail")
	if got != "tail" {
		t.Fatalf("got %q", got)
	}
}

func TestStripUnsafeANSIDropsNonSGRCSIKeepsSGR(t *testing.T) {
	// Colours survive, so cargo/jest output stays coloured.
	sgr := "a\x1b[31mred\x1b[0mb\x1b[1;32mgreen\x1b[0m"
	if got := StripUnsafeANSI(sgr); got != sgr {
		t.Fatalf("SGR mangled: %q", got)
	}
	// Cursor movement, screen clear, cursor hide do not.
	got := StripUnsafeANSI("x\x1b[2Jy\x1b[?25lz\x1b[Hw\x1b[10;20H")
	if got != "xyzw" {
		t.Fatalf("got %q", got)
	}
}

func TestStripUnsafeANSIDropsShortAndStringEscapes(t *testing.T) {
	// Save/restore cursor, reset, charset designation.
	got := StripUnsafeANSI("a\x1b7b\x1b8c\x1bcd\x1b(B")
	if got != "abcd" {
		t.Fatalf("got %q", got)
	}
	// DCS payload does not leak.
	got = StripUnsafeANSI("x\x1bP1$y\x1b\\z")
	if got != "xz" {
		t.Fatalf("got %q", got)
	}
}

// An unterminated OSC must swallow the rest of the string exactly as a
// terminal would — but inertly: nothing after it may come out as a live
// sequence, and no bare ESC may ever be re-emitted, or two chunks of a
// stream could reassemble into one working clipboard write.
func TestStripUnsafeANSIUnterminatedAndDangling(t *testing.T) {
	got := StripUnsafeANSI("ok\x1b]52;c;aGVsbG8=")
	if got != "ok]52;c;aGVsbG8=" {
		t.Fatalf("got %q", got)
	}
	got = StripUnsafeANSI("ok\x1b")
	if got != "ok" {
		t.Fatalf("dangling ESC survived: %q", got)
	}
	got = StripUnsafeANSI("ok\x1b[")
	if got != "ok[" {
		t.Fatalf("dangling CSI survived: %q", got)
	}
	if strings.ContainsRune(StripUnsafeANSI("a\x1b]b"), 0x1b) {
		t.Fatal("a re-emitted ESC can complete across chunk boundaries")
	}
}

func TestStripUnsafeANSIPlainTextUnchanged(t *testing.T) {
	for _, s := range []string{"", "plain text", "tabs\tand\nnewlines", "é42%"} {
		if got := StripUnsafeANSI(s); got != s {
			t.Fatalf("%q became %q", s, got)
		}
	}
}
