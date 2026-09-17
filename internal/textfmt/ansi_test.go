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

// TestStripUnsafeANSIC1Forms: a terminal accepts three spellings of the
// same sequence, and only ESC-prefixed ones were recognised. The fast
// path returned early on any text with no ESC in it at all, so an OSC 52
// clipboard write spelled with U+009D went through untouched.
func TestStripUnsafeANSIC1Forms(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"utf-8 CSI erase display", "a\u009b2Jb", "ab"},
		{"utf-8 OSC 52 clipboard", "a\u009d52;c;aGk=\x07b", "ab"},
		{"utf-8 OSC ended by C1 ST", "a\u009d0;title\u009cb", "ab"},
		{"utf-8 DCS", "a\u00901$y\u009cb", "ab"},
		{"raw 8-bit CSI", "a\x9b2Jb", "ab"},
		{"raw 8-bit OSC", "a\x9d52;c;aGk=\x07b", "ab"},
		{"lone C1 control", "a\u0085b", "ab"},
	} {
		got := StripUnsafeANSI(tc.in)
		if got != tc.want {
			t.Errorf("%s: StripUnsafeANSI(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if strings.ContainsAny(got, "\u009b\u009d\u0090") {
			t.Errorf("%s: a C1 introducer survived", tc.name)
		}
	}
}

// TestStripUnsafeANSIPrivateCSIIsNotColour: the keep test was "the final
// byte is m". CSI > 4 ; 2 m ends in m and is xterm's modifyOtherKeys —
// it changes how the terminal encodes keys, which is state, not colour.
func TestStripUnsafeANSIPrivateCSIIsNotColour(t *testing.T) {
	for _, in := range []string{"\x1b[>4;2m", "\x1b[?1049m", "\x1b[<1m", "\x1b[=3m"} {
		if got := StripUnsafeANSI("a" + in + "b"); got != "ab" {
			t.Errorf("StripUnsafeANSI(%q) kept a private-parameter sequence: %q", in, got)
		}
	}
	// Ordinary SGR still survives, which is the point of keeping any of it.
	for _, in := range []string{"\x1b[0m", "\x1b[1;31m", "\x1b[38;2;10;20;30m"} {
		if got := StripUnsafeANSI("a" + in + "b"); got != "a"+in+"b" {
			t.Errorf("StripUnsafeANSI(%q) dropped plain colour: %q", in, got)
		}
	}
}

// TestStripUnsafeANSIAbortsOnAControl: swallowing an OSC payload to the
// next terminator ate the rest of the line, so a model writing prose
// about escape sequences lost text it never meant as one. A real
// terminal aborts the sequence on a C0 control.
func TestStripUnsafeANSIAbortsOnAControl(t *testing.T) {
	got := StripUnsafeANSI("a\x1b]0;ti\ntle\x07b")
	if !strings.Contains(got, "\n") || !strings.Contains(got, "tle") {
		t.Fatalf("the newline and the text after it were swallowed: %q", got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("an ESC survived: %q", got)
	}
}

// TestStripUnsafeANSISurvivesEveryChunkBoundary: the stripper is
// stateless, so the guarantee is that no split can reassemble a live
// sequence out of two inert halves.
func TestStripUnsafeANSISurvivesEveryChunkBoundary(t *testing.T) {
	for _, payload := range []string{
		"x\x1b]52;c;aGk=\x07y",
		"x\u009d52;c;aGk=\x07y",
		"x\x1b[>4;2my",
		"x\u009b2Jy",
	} {
		for i := 0; i <= len(payload); i++ {
			out := StripUnsafeANSI(payload[:i]) + StripUnsafeANSI(payload[i:])
			for _, bad := range []rune{0x1b, 0x9b, 0x9d, 0x90, 0x9e, 0x9f, 0x98} {
				if strings.ContainsRune(out, bad) {
					t.Fatalf("split %q at %d left %q, which carries an introducer", payload, i, out)
				}
			}
		}
	}
}

// TestStripUnsafeANSILeavesRealTextAlone: over-stripping is its own bug.
func TestStripUnsafeANSILeavesRealTextAlone(t *testing.T) {
	for _, s := range []string{
		"a € b",          // 0x82 is the second byte of the euro sign
		"café ©2026 ±3°", // 0xc2 leads many Latin-1 supplement runes
		"日本語のテキスト",
		"a\u00a0b", // no-break space: 0xc2 0xa0, not a C1
	} {
		if out := StripUnsafeANSI(s); out != s {
			t.Errorf("StripUnsafeANSI(%q) = %q", s, out)
		}
	}
}
