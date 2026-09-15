// Package textfmt holds small text-formatting helpers shared across the
// CLI, TUI, web UI and agent packages: pluralizing counts, shortening
// paths, durations, ages, byte counts and token counts for status lines,
// and clipping or flattening strings for display. It imports only the
// standard library.
package textfmt

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Plural renders n with word, pluralized with a trailing "s" unless n is 1.
func Plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// RelTo renders p relative to cwd when that stays within cwd; otherwise it
// returns p unchanged.
func RelTo(cwd, p string) string {
	if cwd == "" || !filepath.IsAbs(p) {
		return p
	}
	if r, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return p
}

// Tokens renders a token count compactly: "12k", "1.5k", "2.3M".
func Tokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// Duration renders d compactly: "<1ms", "420ms", "1.5s", or the rounded
// Go duration string once it reaches a minute.
func Duration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "<1ms"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

// Age renders d as a coarse age: minutes, then hours, then days.
func Age(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Bytes renders a byte count compactly: "512B", "1.5KB", "2.0MB".
func Bytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

// Clip shortens s to at most n runes, marking a cut with a trailing "…".
// It never splits a multi-byte rune. n<=0 clips to nothing.
func Clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// FirstLine returns the text up to the first newline, clipped to at most
// max runes with "…" when max > 0. max<=0 means no cap.
func FirstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if max <= 0 {
		return s
	}
	return Clip(s, max)
}

// ClipBytes shortens s to at most max bytes, never splitting a multi-byte
// UTF-8 character. When the string is clipped a trailing "…" is appended;
// the result is always valid UTF-8 and at most max bytes long.
func ClipBytes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	const ellipsis = "…"
	if max < len(ellipsis) {
		// No room for the ellipsis: keep what fits, still whole runes.
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut]
	}
	cut := max - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// OneLine flattens s to a single line by replacing newlines with spaces
// and trimming the result.
func OneLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}

// Squash collapses every run of whitespace in s, newlines included, to a
// single space and trims both ends.
func Squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// SplitCSV splits a comma-separated list, trimming each item and dropping
// empty ones; nil when nothing is left.
func SplitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// HeadingLine takes the first line of s, strips any leading Markdown
// heading markers ("#" and spaces), and clips the result to at most max
// runes (see Clip). max<=0 means no cap.
func HeadingLine(s string, max int) string {
	line, _, _ := strings.Cut(s, "\n")
	line = strings.TrimSpace(strings.TrimLeft(line, "# "))
	if max <= 0 {
		return line
	}
	return Clip(line, max)
}

// MaxOutput is the hard cap on what a tool returns, in bytes. The agent
// archives anything over its inline limit, so this only bounds memory.
const MaxOutput = 256 * 1024

// Truncate clips s to about MaxOutput bytes, keeping the head and the tail
// with a marker in between: the failure in a long test run is at the end.
func Truncate(s string) string {
	return TruncateTo(s, MaxOutput)
}

// TruncateTo is Truncate with an explicit byte budget.
func TruncateTo(s string, max int) string {
	if len(s) <= max {
		return s
	}
	head, tail := max*2/3, max/3
	// Cut on line boundaries where possible so no line is left half-shown.
	if i := strings.LastIndexByte(s[:head], '\n'); i > head/2 {
		head = i
	}
	if i := strings.IndexByte(s[len(s)-tail:], '\n'); i >= 0 && i < tail/2 {
		tail -= i + 1
	}
	return s[:head] + fmt.Sprintf("\n... [%d bytes omitted; the output continues] ...\n", len(s)-head-tail) + s[len(s)-tail:]
}
