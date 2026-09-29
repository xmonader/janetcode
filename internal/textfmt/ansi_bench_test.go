package textfmt

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// frame is roughly what the TUI sends a terminal each render: coloured
// rows, box-drawing borders and some non-ASCII prose.
func benchFrame() string {
	row := "\x1b[38;5;245m│\x1b[0m \x1b[1mtool\x1b[0m output — naïve café ─────── 日本語 text here and more text to fill a line \x1b[32mok\x1b[0m\n"
	return strings.Repeat(row, 60)
}

// refStrip is the rune-at-a-time loop StripUnsafeANSI used before plain
// runs were copied whole; the two must agree on every input.
func refStrip(s string) string {
	if !mayCarryEscape(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		n, keep, ok := escapeAt(s[i:])
		if !ok {
			_, size := utf8.DecodeRuneInString(s[i:])
			b.WriteString(s[i : i+size])
			i += size
			continue
		}
		if n == 0 {
			i += introducerDrop(s[i:])
			continue
		}
		if keep {
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

func FuzzStripUnsafeANSIMatchesReference(f *testing.F) {
	f.Add(benchFrame())
	f.Add("a\x1b]52;c;aGk=\x07b\x1b[2Jc\u009b31md\xc2\x9de\x9bf€─\x1b")
	f.Add("\x1b[>4;2m\x1bP1$r\x1b\\x\x1b(B\x1b7")
	f.Fuzz(func(t *testing.T, s string) {
		if got, want := StripUnsafeANSI(s), refStrip(s); got != want {
			t.Fatalf("StripUnsafeANSI(%q) = %q, reference %q", s, got, want)
		}
	})
}

func BenchmarkStripUnsafeANSIFrame(b *testing.B) {
	s := benchFrame()
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = StripUnsafeANSI(s)
	}
}

func BenchmarkStripUnsafeANSIASCII(b *testing.B) {
	s := strings.Repeat("plain ascii output line with nothing in it at all\n", 60)
	b.SetBytes(int64(len(s)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = StripUnsafeANSI(s)
	}
}
