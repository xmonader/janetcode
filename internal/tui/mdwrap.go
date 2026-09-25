package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// glamour wraps a list item's text back to the list's own margin, so the
// second line of a bullet starts under the bullet rather than under the
// words, and it drops the bar from every wrapped line of a quote after
// the first. hangMarkdown puts both right on glamour's output: a group
// made of an item (or a quote line) and the lines glamour wrapped from
// it is joined and wrapped again under the item's text, or behind the
// quote's bar. width is the width glamour wrapped to. Anything else, and
// any block holding a fence or a hard break, is returned as it is.
func hangMarkdown(src, out string, width int) string {
	// A fence's lines are not prose, and a hard break (two trailing
	// spaces, or a backslash) is a line the writer meant to keep.
	if strings.Contains(src, "```") || strings.Contains(src, "~~~") ||
		strings.Contains(src, "  \n") || strings.Contains(src, "\\\n") {
		return out
	}
	lead := strings.TrimLeft(src, " \t")
	quote := strings.HasPrefix(lead, ">")
	if !quote && !mdListSrc.MatchString(src) {
		return out
	}
	lines := strings.Split(out, "\n")
	res := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		plain := ansi.Strip(lines[i])
		m := mdItemHead.FindStringSubmatch(plain)
		if quote {
			m = mdQuoteHead.FindStringSubmatch(plain)
		}
		if m == nil || strings.TrimSpace(plain) == "│" {
			res = append(res, lines[i])
			i++
			continue
		}
		indent := len(m[1])
		j := i + 1
		for j < len(lines) && isContinuation(ansi.Strip(lines[j]), indent, quote) {
			j++
		}
		if j == i+1 {
			res = append(res, lines[i])
			i++
			continue
		}
		joined := trimStyledRight(lines[i])
		for _, l := range lines[i+1 : j] {
			if quote {
				l = dropQuoteBar(l)
			}
			joined += " " + trimStyledRight(trimStyledLeft(l))
		}
		hang := strings.Repeat(" ", ansi.StringWidth(m[0]))
		if quote {
			hang = strings.Repeat(" ", indent) + m[2]
		}
		res = append(res, hangWrap(joined, width, hang))
		i = j
	}
	return strings.Join(res, "\n")
}

var (
	// A block that is a list: its first line starts an item.
	mdListSrc = regexp.MustCompile(`^\s*([-*+]|\d+[.)])\s`)
	// How glamour draws an item's first line, and a quote's.
	mdItemHead  = regexp.MustCompile(`^(\s*)(• |\d+\. )`)
	mdQuoteHead = regexp.MustCompile(`^(\s*)(│ )`)
	// The run of colour codes and spaces at either end of a line glamour
	// padded to the full width.
	mdStyledRight = regexp.MustCompile(`(?:\x1b\[[0-9;]*m| )+$`)
	mdStyledLeft  = regexp.MustCompile(`^(?:\x1b\[[0-9;]*m| )+`)
)

// isContinuation reports whether a line glamour drew after an item (or a
// quote line) at indent is more of the same text: not blank, not a new
// item, and at the item's indent.
func isContinuation(plain string, indent int, quote bool) bool {
	if strings.TrimSpace(plain) == "" {
		return false
	}
	if quote {
		// Every line of a quote paragraph joins, barred or not; a bar
		// alone is the gap between two paragraphs.
		return strings.TrimSpace(plain) != "│" && leadingSpaces(plain) >= indent
	}
	if mdItemHead.MatchString(plain) {
		return false
	}
	return leadingSpaces(plain) == indent
}

func leadingSpaces(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// trimStyledRight drops the padding glamour added after the text, keeping
// a reset so no colour runs on.
func trimStyledRight(s string) string {
	return mdStyledRight.ReplaceAllString(s, "") + "\x1b[0m"
}

// trimStyledLeft drops the indent before the text but keeps the colour
// codes in it, so a wrapped line that starts inside a coloured span
// stays coloured.
func trimStyledLeft(s string) string {
	head := mdStyledLeft.FindString(s)
	return strings.ReplaceAll(head, " ", "") + s[len(head):]
}

// dropQuoteBar removes a bar glamour did draw on a wrapped quote line, so
// the rewrapped group has exactly one bar per line.
func dropQuoteBar(s string) string {
	return strings.Replace(s, "│ ", "", 1)
}
