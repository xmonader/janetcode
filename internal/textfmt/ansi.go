package textfmt

import "strings"

// StripUnsafeANSI removes the escape sequences that change terminal
// state from text a terminal will eventually display — OSC sequences
// (clipboard writes, window titles, hyperlinks, notification zones) and
// every CSI sequence that is not SGR colour (cursor movement, private
// modes, clears). SGR sequences are kept, so command output keeps its
// colours through a headless pipe.
//
// It exists because headless mode prints model deltas and tool output
// verbatim, and a hostile file read into that output could otherwise
// steal the clipboard (OSC 52) or repaint the caller's terminal.
func StripUnsafeANSI(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			i++
			continue
		}
		n, keep := escapeAt(s[i:])
		if n == 0 {
			// Malformed, dangling, or unterminated: drop the ESC itself.
			// Never re-emit it — a lone ESC here can join a later chunk
			// into a live sequence, which is exactly what is being
			// prevented. The remaining bytes stay as inert visible text.
			i++
			continue
		}
		if keep {
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// escapeAt reports the escape sequence at the start of s: its length
// and whether it is display-safe (SGR colour). n is 0 when s does not
// begin with a complete recognised sequence — including a lone ESC with
// nothing valid after it, or an unterminated string sequence — and the
// caller drops the ESC so no later text can complete it into a live
// sequence.
func escapeAt(s string) (n int, keep bool) {
	if len(s) < 2 {
		return 0, false
	}
	switch s[1] {
	case ']': // OSC: payload terminated by BEL or ST (ESC \).
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1, false
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2, false
			}
		}
		return 0, false // unterminated: everything to the end is payload
	case '[': // CSI: params, intermediates, one final byte.
		i := 2
		for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
			i++
		}
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i >= len(s) || s[i] < 0x40 || s[i] > 0x7e {
			return 0, false
		}
		return i + 1, s[i] == 'm' // SGR kept, everything else dropped
	case 'P', '_', '^', 'X': // DCS, APC, PM, SOS: string classes like OSC.
		for i := 2; i < len(s); i++ {
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2, false
			}
		}
		return 0, false
	default: // Two-byte escapes and those with intermediates: ESC 7, ESC (B, ESC c.
		i := 1
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
			i++
		}
		if i >= len(s) || s[i] < 0x30 || s[i] > 0x7e {
			return 0, false
		}
		return i + 1, false
	}
}
