package textfmt

import "strings"

// Display is text as a terminal would end up showing it: what a terminal
// would end up showing for each line, with no control character left to
// move the cursor. A carriage return is how every progress bar redraws
// itself (git clone, curl, npm, cargo), and in a tool's output it reached
// the screen as it was: the cursor went back to the left edge mid-frame
// and the rest of the line — the indent, the styling, the start of the
// text — was drawn over. A line's \r-separated parts are laid over each
// other the way the terminal would have laid them, a backspace steps back
// one, and the other C0 controls (BEL, vertical tab, form feed…) are
// dropped. Tabs, newlines and escape sequences are left for the
// renderers and the frame filter that handle them.
func Display(s string) string {
	// Escapes first, whatever else the text holds: headless prints what
	// this returns with no frame filter after it.
	s = StripUnsafeANSI(s)
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\n' && c != '\t' && c != 0x1b) || c == 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	// The sequences the frame filter would drop go first: a BEL is also
	// what ends an OSC, and dropping it here would leave the filter an
	// unterminated sequence whose payload it shows as text.
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = overstrike(strings.TrimRight(l, "\r"))
	}
	return strings.Join(lines, "\n")
}

// overstrike plays one line's carriage returns and backspaces onto a row
// of cells. A line that also carries escape sequences cannot be laid cell
// by cell without parsing them, so it keeps its last non-empty part after
// a carriage return, which is what a redrawn progress line ends on.
func overstrike(l string) string {
	if strings.IndexByte(l, 0x1b) >= 0 {
		if i := strings.LastIndexByte(l, '\r'); i >= 0 {
			l = l[i+1:]
		}
		return strings.Map(func(r rune) rune {
			if (r < 0x20 && r != '\t' && r != 0x1b) || r == 0x7f {
				return -1
			}
			return r
		}, l)
	}
	var row []rune
	col := 0
	for _, r := range l {
		switch {
		case r == '\r':
			col = 0
		case r == '\b':
			if col > 0 {
				col--
			}
		case (r < 0x20 && r != '\t') || r == 0x7f:
			// dropped
		default:
			if col < len(row) {
				row[col] = r
			} else {
				row = append(row, r)
			}
			col++
		}
	}
	return string(row)
}
