package tool

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/diff"
)

// An edit locates itself by quoting the text it replaces, and a model
// quoting text it read a moment ago still gets it wrong in ways that have
// nothing to do with the code: an em dash where the file has a hyphen, a
// curly quote where the file has a straight one, a non-breaking space
// inside an indent, a trailing space the file does not have. Refusing
// those costs a round trip to be told something that was never ambiguous.
//
// So a miss is not the end. After the exact match fails, the same search
// is run again over a folded view of both sides, and then again ignoring
// trailing whitespace on each line. The ladder is deterministic and each
// rung is strictly more forgiving than the last; leading whitespace is
// never relaxed, because in Python and YAML it is the program.

// foldRune maps a character to the one it stands in for, or itself.
func foldRune(r rune) rune {
	switch r {
	case '‐', '‑', '‒', '–', '—', '―', '−', '﹘', '﹣', '－':
		return '-'
	case '‘', '’', '‚', '‛', '′', '‵', '＇':
		return '\''
	case '“', '”', '„', '‟', '″', '‶', '＂':
		return '"'
	case '\u00a0', '\u1680', '\u2000', '\u2001', '\u2002', '\u2003',
		'\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009',
		'\u200a', '\u202f', '\u205f', '\u3000':
		return ' '
	}
	return r
}

// folded is a string with every equivalent character replaced by its
// representative, plus the map back to byte offsets in the original.
type folded struct {
	text string
	// at[i] is the byte offset in the original of the i-th byte of text.
	// It has len(text)+1 entries; the last is len(original).
	at []int
}

func fold(s string) folded {
	var b strings.Builder
	b.Grow(len(s))
	at := make([]int, 0, len(s)+1)
	for i, r := range s {
		f := foldRune(r)
		n := utf8.RuneLen(f)
		for j := 0; j < n; j++ {
			at = append(at, i)
		}
		b.WriteRune(f)
	}
	at = append(at, len(s))
	return folded{text: b.String(), at: at}
}

// matchFolded finds occurrences of old in content ignoring the character
// equivalences, and returns their spans as byte ranges in content.
func matchFolded(content, old string) [][2]int {
	fc, fo := fold(content), fold(old)
	if fo.text == "" {
		return nil
	}
	var spans [][2]int
	for i := 0; ; {
		j := strings.Index(fc.text[i:], fo.text)
		if j < 0 {
			break
		}
		start := i + j
		end := start + len(fo.text)
		spans = append(spans, [2]int{fc.at[start], fc.at[end]})
		i = start + 1
	}
	return spans
}

// lineSpans is content split into lines with their byte offsets; the span
// of line i is [starts[i], starts[i+1]) including its newline.
type lineSpans struct {
	lines  []string
	starts []int
}

func splitLineSpans(s string) lineSpans {
	var ls lineSpans
	off := 0
	for off <= len(s) {
		i := strings.IndexByte(s[off:], '\n')
		if i < 0 {
			ls.lines = append(ls.lines, s[off:])
			ls.starts = append(ls.starts, off)
			break
		}
		ls.lines = append(ls.lines, s[off:off+i])
		ls.starts = append(ls.starts, off)
		off += i + 1
	}
	ls.starts = append(ls.starts, len(s))
	return ls
}

// key is a line reduced to what the last rung of the ladder compares:
// folded, with trailing whitespace removed. Leading whitespace is kept.
func key(line string) string {
	return fold(strings.TrimRight(line, " \t\r")).text
}

// matchLines finds whole-line windows of content that equal old's lines
// once folded and right-trimmed, and returns their byte spans. old must
// be a run of whole lines for this to be tried at all: a partial line
// cannot be compared this way without guessing where it ends.
func matchLines(content, old string) [][2]int {
	if old == "" {
		return nil
	}
	trailing := strings.HasSuffix(old, "\n")
	oldLines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	cs := splitLineSpans(content)
	want := make([]string, len(oldLines))
	for i, l := range oldLines {
		want[i] = key(l)
	}
	var spans [][2]int
	for i := 0; i+len(want) <= len(cs.lines); i++ {
		ok := true
		for j := range want {
			if key(cs.lines[i+j]) != want[j] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		start := cs.starts[i]
		end := cs.starts[i+len(want)]
		if !trailing {
			// The quoted text did not include the final newline, so the
			// replacement must not swallow it either.
			end = cs.starts[i+len(want)-1] + len(cs.lines[i+len(want)-1])
		}
		spans = append(spans, [2]int{start, end})
	}
	return spans
}

// dropOverlaps keeps the first of any spans that overlap, the way a
// left-to-right replace does: repeated lines can make two windows claim
// the same bytes, and rewriting both would corrupt the file.
func dropOverlaps(spans [][2]int) [][2]int {
	out := spans[:0]
	end := -1
	for _, sp := range spans {
		if sp[0] < end {
			continue
		}
		out = append(out, sp)
		end = sp[1]
	}
	return out
}

// replaceSpans rewrites content at the given byte ranges, which must be
// sorted and non-overlapping.
func replaceSpans(content string, spans [][2]int, with string) string {
	var b strings.Builder
	prev := 0
	for _, sp := range spans {
		b.WriteString(content[prev:sp[0]])
		b.WriteString(with)
		prev = sp[1]
	}
	b.WriteString(content[prev:])
	return b.String()
}

// nearMiss describes the region of content that comes closest to old, as a
// diff, so a failed edit says what is actually there instead of only that
// nothing matched. It returns "" when nothing in the file is close enough
// for the comparison to mean anything.
func nearMiss(content, old, display string) string {
	oldLines := strings.Split(strings.TrimSuffix(old, "\n"), "\n")
	cs := splitLineSpans(content)
	if len(cs.lines) == 0 || len(oldLines) == 0 {
		return ""
	}
	n := len(oldLines)
	if n > len(cs.lines) {
		n = len(cs.lines)
	}
	best, bestScore, bestAt := "", 0.0, 0
	for i := 0; i+n <= len(cs.lines); i++ {
		window := cs.lines[i : i+n]
		s := similarity(window, oldLines)
		if s > bestScore {
			best, bestScore, bestAt = strings.Join(window, "\n"), s, i
		}
	}
	// Below a third of the lines in common the "closest region" is noise,
	// and printing it would be worse than saying nothing.
	if bestScore < 0.34 {
		return ""
	}
	d := diff.Unified(fmt.Sprintf("%s:%d", display, bestAt+1), old, best, 0)
	if strings.TrimSpace(d) == "" {
		return ""
	}
	return fmt.Sprintf("The closest thing in the file is at line %d; this is what you asked for (-) against what is there (+):\n%s", bestAt+1, strings.TrimRight(d, "\n"))
}

// similarity is the share of old's lines that appear, right-trimmed and
// folded, somewhere in the window.
func similarity(window, old []string) float64 {
	have := map[string]int{}
	for _, l := range window {
		have[key(l)]++
	}
	hits := 0
	for _, l := range old {
		k := key(l)
		if have[k] > 0 {
			have[k]--
			hits++
		}
	}
	return float64(hits) / float64(len(old))
}
