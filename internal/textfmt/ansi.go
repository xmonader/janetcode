package textfmt

import (
	"strings"
	"unicode/utf8"
)

// StripUnsafeANSI removes the escape sequences that change terminal
// state from text a terminal will eventually display — OSC sequences
// (clipboard writes, window titles, hyperlinks, notification zones) and
// every CSI sequence that is not SGR colour (cursor movement, private
// modes, clears). Plain SGR sequences are kept, so command output keeps
// its colours through a headless pipe.
//
// It exists because headless mode prints model deltas and tool output
// verbatim, and a hostile file read into that output could otherwise
// steal the clipboard (OSC 52) or repaint the caller's terminal.
//
// It is stateless on purpose. An incomplete sequence has its introducer
// dropped and the remainder emitted as inert text, so splitting a stream
// anywhere cannot join two halves into a live sequence — the classic way
// a per-chunk stripper is defeated.
func StripUnsafeANSI(s string) string {
	if !mayCarryEscape(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	// The TUI puts every frame through this, and a frame is mostly text
	// with box drawing and colour in it — enough to miss the fast path
	// above every time. So plain runs are copied whole when an escape
	// ends them, and only a byte that could begin one is looked at
	// closely: ESC, a raw C1, or 0xc2, the lead byte of U+0080..U+009F.
	start := 0 // where the plain run not yet written begins
	for i := 0; i < len(s); {
		if c := s[i]; c < 0x80 && c != 0x1b {
			i++
			continue
		} else if c >= 0xa0 && c != 0xc2 {
			// The lead byte of a rune that is not a C1. Advance a whole
			// rune: stepping a byte at a time would land on a continuation
			// byte and read it as a control — "€" is 0xe2 0x82 0xac, and
			// 0x82 is in the C1 range.
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
			continue
		}
		n, keep, ok := escapeAt(s[i:])
		if !ok {
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
			continue
		}
		b.WriteString(s[start:i])
		if n == 0 {
			// Malformed, dangling, or unterminated: drop the introducer
			// itself. Never re-emit it — a lone introducer here can join
			// a later chunk into a live sequence, which is exactly what
			// is being prevented. The rest stays as inert visible text.
			i += introducerDrop(s[i:])
			start = i
			continue
		}
		if keep {
			b.WriteString(s[i : i+n])
		}
		i += n
		start = i
	}
	b.WriteString(s[start:])
	return b.String()
}

// mayCarryEscape is the fast path: text with no ESC and no C1 control
// cannot carry a sequence. A C1 can arrive as a raw byte or as the UTF-8
// encoding of U+0080..U+009F, whose lead byte is always 0xc2.
func mayCarryEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == 0x1b || (c >= 0x80 && c <= 0x9f) || c == 0xc2 {
			return true
		}
	}
	return false
}

// introducer reports the escape introducer at the start of s: which
// control it names, spelled as the byte that follows ESC in the 7-bit
// form, and how many bytes it occupies.
//
// A terminal accepts three spellings of the same sequence: ESC [, the
// single byte 0x9b, and the UTF-8 encoding of U+009B, which xterm and
// VTE decode back to CSI. Only the first was recognised, so an OSC 52
// clipboard write spelled with U+009D passed through whole.
func introducer(s string) (kind byte, n int, ok bool) {
	if len(s) == 0 {
		return 0, 0, false
	}
	if s[0] == 0x1b {
		if len(s) < 2 {
			return 0, 1, true // a dangling ESC: an introducer with nothing after it
		}
		return s[1], 2, true
	}
	// A C1 written as UTF-8. Decoding rather than byte-matching matters:
	// the second byte of "€" (0xe2 0x82 0xac) is 0x82, which is in the C1
	// range but is not a control.
	if r, size := utf8.DecodeRuneInString(s); r >= 0x80 && r <= 0x9f {
		return c1Kind(byte(r)), size, true
	} else if r == utf8.RuneError && size == 1 && s[0] >= 0x80 && s[0] <= 0x9f {
		// A raw 8-bit control, which a terminal not in UTF-8 mode reads
		// as C1. Invalid UTF-8 either way, so nothing legitimate is lost.
		return c1Kind(s[0]), 1, true
	}
	return 0, 0, false
}

// introducerDrop is how many bytes to remove when no complete sequence
// follows: the control itself and nothing more. For the 7-bit form that
// is the ESC alone — the byte after it is ordinary text and stays
// visible, so "ESC ] 52;c;..." with no terminator reads back as
// "]52;c;...". For a C1 the whole encoding is the control.
func introducerDrop(s string) int {
	if len(s) > 0 && s[0] == 0x1b {
		return 1
	}
	_, n, ok := introducer(s)
	if !ok || n == 0 {
		return 1
	}
	return n
}

// c1Kind maps a C1 control to the byte that spells the same sequence
// after ESC: 0x9b is CSI, which ESC [ also is.
func c1Kind(b byte) byte {
	switch b {
	case 0x9b:
		return '['
	case 0x9d:
		return ']'
	case 0x90:
		return 'P'
	case 0x9e:
		return '^'
	case 0x9f:
		return '_'
	case 0x98:
		return 'X'
	}
	// Any other C1 is a lone control with no payload — NEL, IND, RI, and
	// the string terminator. None of them are content.
	return 0
}

// escapeAt reports the escape sequence at the start of s: its length and
// whether it is display-safe (plain SGR colour). ok is false when s does
// not begin with an introducer at all. n is 0 when it begins with one
// that no complete sequence follows, and the caller drops the introducer
// so no later text can complete it.
func escapeAt(s string) (n int, keep, ok bool) {
	kind, head, ok := introducer(s)
	if !ok {
		return 0, false, false
	}
	if head >= len(s) {
		return 0, false, true
	}
	switch kind {
	case ']', 'P', '^', '_', 'X': // OSC and the other string classes.
		for i := head; i < len(s); {
			if t := terminatorAt(s[i:]); t > 0 {
				return i + t, false, true
			}
			// A C0 control other than ESC aborts a string sequence on a
			// real terminal. Swallowing to the next BEL ate the rest of
			// the line, so a model writing prose *about* ANSI escapes
			// lost text it never meant as one.
			if c := s[i]; c < 0x20 && c != 0x1b {
				return i, false, true
			}
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
		}
		return 0, false, true // unterminated: everything to the end is payload
	case '[': // CSI: parameters, intermediates, one final byte.
		i := head
		private := false
		for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
			// 0x3c..0x3f are private-parameter bytes. CSI > 4 ; 2 m is
			// xterm's modifyOtherKeys — it ends in 'm' but sets the
			// terminal's key encoding, not a colour.
			if s[i] >= 0x3c {
				private = true
			}
			i++
		}
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i >= len(s) || s[i] < 0x40 || s[i] > 0x7e {
			return 0, false, true
		}
		return i + 1, s[i] == 'm' && !private, true
	case 0:
		// A lone C1 control with no payload: drop the control itself.
		return head, false, true
	default: // Two-byte escapes and those with intermediates: ESC 7, ESC (B, ESC c.
		i := head - 1 // the byte after ESC is part of what is scanned
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i >= len(s) || s[i] < 0x30 || s[i] > 0x7e {
			return 0, false, true
		}
		return i + 1, false, true
	}
}

// terminatorAt reports the length of a string-sequence terminator at the
// start of s: BEL, ESC \, or ST in either of its C1 spellings.
func terminatorAt(s string) int {
	switch {
	case len(s) == 0:
		return 0
	case s[0] == 0x07:
		return 1
	case s[0] == 0x1b && len(s) > 1 && s[1] == '\\':
		return 2
	case s[0] == 0x9c:
		return 1
	case s[0] == 0xc2 && len(s) > 1 && s[1] == 0x9c:
		return 2
	}
	return 0
}
