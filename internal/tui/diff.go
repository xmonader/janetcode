package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// A diff is the densest thing the transcript shows, and it was the least
// legible: every line coloured by its leading + or -, no line numbers, no
// hint of how far apart two hunks were, and for a one-word rename two
// whole lines in red and green to compare by eye.
//
// What is drawn now, from the unified diff the edit tools already
// produce:
//
//	  12 │  func Add(a, b int) int {
//	  13 │ -	return a + b
//	  13 │ +	return a - b
//	     … 40 unchanged lines
//	  54 │  }
//
// The gutter is sized to the file, hunks are separated by a count of what
// lies between them, each hunk keeps three lines of context, and when a
// hunk is one line replaced by one line only the tokens that differ are
// picked out: each changed line is tinted across the width, and the words
// that changed in a stronger tint (inverse video where the theme's colours
// cannot be tinted).

// diffContext is how many unchanged lines are kept around a change.
const diffContext = 3

type diffLine struct {
	kind byte // ' ', '+', '-', or '\\' for a "\ No newline at end of file" marker
	text string
}

// marker reports whether l is a note about the line before it rather
// than a line of either file: it takes no line number.
func (l diffLine) marker() bool { return l.kind == '\\' }

type diffHunk struct {
	oldStart, newStart int
	lines              []diffLine
}

// parseUnifiedDiff reads the hunks of a unified diff. It reports false for
// anything that is not one, so a plain preview still renders as text.
func parseUnifiedDiff(s string) ([]diffHunk, bool) {
	var hunks []diffHunk
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		switch {
		case len(hunks) == 0 && (strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ")):
			// The file name is already on the tool line.
		case strings.HasPrefix(l, "@@"):
			o, n, ok := parseHunkHeader(l)
			if !ok {
				return nil, false
			}
			hunks = append(hunks, diffHunk{oldStart: o, newStart: n})
		case len(hunks) == 0:
			return nil, false // content before any hunk header
		default:
			kind := byte(' ')
			text := l
			if l != "" && (l[0] == '+' || l[0] == '-' || l[0] == ' ' || l[0] == '\\') {
				kind, text = l[0], l[1:]
			}
			h := &hunks[len(hunks)-1]
			h.lines = append(h.lines, diffLine{kind, text})
		}
	}
	return hunks, len(hunks) > 0
}

// parseHunkHeader reads the old and new start lines out of "@@ -1,2 +1,3 @@".
func parseHunkHeader(l string) (old, new int, ok bool) {
	f := strings.Fields(l)
	if len(f) < 3 || !strings.HasPrefix(f[1], "-") || !strings.HasPrefix(f[2], "+") {
		return 0, 0, false
	}
	num := func(s string) (int, bool) {
		s = s[1:]
		if i := strings.IndexByte(s, ','); i >= 0 {
			s = s[:i]
		}
		n, err := strconv.Atoi(s)
		return n, err == nil
	}
	o, ok1 := num(f[1])
	n, ok2 := num(f[2])
	return o, n, ok1 && ok2
}

// trimContext keeps at most diffContext unchanged lines at each end of a
// hunk, adjusting the start lines for what it dropped.
func (h diffHunk) trimContext() diffHunk {
	first, last := -1, -1
	for i, l := range h.lines {
		if l.kind != ' ' && !l.marker() {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return h
	}
	from := max(0, first-diffContext)
	to := min(len(h.lines), last+diffContext+1)
	for _, l := range h.lines[:from] {
		if !l.marker() {
			h.oldStart++
			h.newStart++
		}
	}
	h.lines = h.lines[from:to]
	return h
}

// changePair reports the single old and new line of a hunk that replaces
// exactly one line with exactly one line — the case worth a word-level
// comparison.
func (h diffHunk) changePair() (int, int, bool) {
	del, add := -1, -1
	for i, l := range h.lines {
		switch l.kind {
		case '-':
			if del >= 0 {
				return 0, 0, false
			}
			del = i
		case '+':
			if add >= 0 {
				return 0, 0, false
			}
			add = i
		}
	}
	if del < 0 || add < del {
		return 0, 0, false
	}
	// Adjacent, but for a no-newline marker after the removed line.
	for _, l := range h.lines[del+1 : add] {
		if !l.marker() {
			return 0, 0, false
		}
	}
	return del, add, true
}

// renderDiff draws the hunks with a gutter, elisions between them and the
// changed tokens picked out where a single line was replaced.
func renderDiff(sty *styles, hunks []diffHunk, width, maxLines int, expandable bool) string {
	// The gutter holds the largest number it will print: a removed line
	// is numbered in the old file, which can run far past the new one.
	last := 0
	for _, h := range hunks {
		last = max(last, h.newStart+len(h.lines), h.oldStart+len(h.lines))
	}
	gw := len(strconv.Itoa(max(last, 1)))
	var out []string
	prevEnd := 0
	for i, raw := range hunks {
		h := raw.trimContext()
		if i > 0 {
			if gap := h.newStart - prevEnd; gap > 0 {
				out = append(out, sty.faint.Render(fmt.Sprintf("  %s … %d unchanged lines", strings.Repeat(" ", gw), gap)))
			}
		}
		hiDel, hiAdd := -1, -1
		if d, a, ok := h.changePair(); ok {
			hiDel, hiAdd = d, a
		}
		oldN, newN := h.oldStart, h.newStart
		tinted := sty.diffTint && !sty.ansiOnly
		for j, l := range h.lines {
			if l.marker() {
				// Faint, under the text column, and with no number: it
				// says the line above has no newline; it is not a line.
				gutter := fmt.Sprintf("  %s │  ", strings.Repeat(" ", gw))
				out = append(out, fitLine(sty.faint.Render(gutter)+sty.faint.Render(`\`+l.text), width-2))
				continue
			}
			n := newN
			style, hi := sty.dim, lipgloss.NewStyle().Reverse(true)
			switch l.kind {
			case '+':
				style, newN = sty.diffAdd, newN+1
				if tinted {
					style, hi = sty.diffAddBg, sty.diffAddHi
				}
			case '-':
				n, style, oldN = oldN, sty.diffDel, oldN+1
				if tinted {
					style, hi = sty.diffDelBg, sty.diffDelHi
				}
			default:
				oldN, newN = oldN+1, newN+1
			}
			if !tinted {
				hi = style.Reverse(true)
			}
			text := l.text
			// What is left of the row for the line's text: the indent and
			// gutter, the +/- mark, and the two columns fitLine keeps free.
			avail := width - 2 - (2 + gw + 3) - 1
			switch j {
			case hiDel:
				text = highlightChanged(l.text, h.lines[hiAdd].text, style, hi, avail)
			case hiAdd:
				text = highlightChanged(l.text, h.lines[hiDel].text, style, hi, avail)
			default:
				text = style.Render(text)
			}
			gutter := sty.faint.Render(fmt.Sprintf("  %*d │ ", gw, n))
			line := fitLine(gutter+style.Render(string(l.kind))+text, width-2)
			if tinted && l.kind != ' ' {
				// The tint runs to the edge, so a change reads as a band.
				if pad := width - 2 - lipgloss.Width(line); pad > 0 {
					line += style.Render(strings.Repeat(" ", pad))
				}
			}
			out = append(out, line)
		}
		prevEnd = newN
	}
	hidden := 0
	if len(out) >= maxLines+clipSlack {
		hidden, out = len(out)-maxLines, out[:maxLines]
	}
	if hidden > 0 {
		out = append(out, sty.faint.Render("  "+moreNote(hidden, expandable)))
	}
	return strings.Join(out, "\n")
}

// highlightChanged renders line with the run of characters that differs
// from other in hi: a stronger tint, or inverse video where there is none.
//
// avail is how many columns the row has for the text. When the change
// lies past them the line is drawn from shortly before it, with a "…"
// standing for what was skipped: the row is cut at the right edge, and on
// a narrow terminal that cut the one word the highlight exists to show —
// both rows of the pair read the same up to the edge and the difference
// was off screen. The two lines of a pair share their prefix up to the
// change, so both skip the same amount and stay aligned.
func highlightChanged(line, other string, style, hi lipgloss.Style, avail int) string {
	line = strings.ReplaceAll(line, "\t", tabStop)
	other = strings.ReplaceAll(other, "\t", tabStop)
	from, to := changedSpan(line, other)
	if from >= to {
		return style.Render(line)
	}
	a := []rune(line)
	if keep := avail / 3; avail > 0 && lipgloss.Width(string(a[:to])) > avail && from > keep+1 {
		skip := from - keep
		a = append([]rune{'…'}, a[skip+1:]...)
		from, to = from-skip, to-skip
	}
	return style.Render(string(a[:from])) +
		hi.Render(string(a[from:to])) +
		style.Render(string(a[to:]))
}

// changedSpan is the half-open rune range of line that differs from
// other. Indentation is never part of it: a line that only moved would
// otherwise light up from its first column, and a token is never cut in
// half.
func changedSpan(line, other string) (from, to int) {
	if line == other {
		return 0, 0
	}
	a, b := []rune(line), []rune(other)
	indent := 0
	for indent < len(a) && (a[indent] == ' ' || a[indent] == '\t') {
		indent++
	}
	p := indent
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	// Widen to token boundaries so half a word is never picked out.
	for p > indent && wordRune(a[p-1]) {
		p--
	}
	for s > 0 && s < len(a)-p && wordRune(a[len(a)-s]) {
		s--
	}
	if p >= len(a)-s {
		return 0, 0
	}
	return p, len(a) - s
}

func wordRune(r rune) bool {
	return r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127
}
