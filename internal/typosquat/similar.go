// Package typosquat checks a dependency name an edit adds against the
// names the project already uses, the names in the local module cache
// and a short list of well-known packages, and flags one that is a
// confusable near-miss of a name it is not: one keystroke away
// (reqeusts), a lookalike letter (jeIlyfish with a capital I), a
// different case (github.com/Sirupsen/logrus), a different separator
// (python_dateutil) or a known name with a decoration bolted on
// (nodemailer-js, boltdb-go/bolt).
//
// It is the offline half of package depcheck. depcheck asks the public
// registry whether a name exists and how old it is, which catches a name
// nobody registered; a typosquat is registered, often for years, and
// looks fine to a registry. What gives it away is its distance from the
// name that was meant, which needs no network at all: the comparison is
// with names already on this machine.
//
// It only flags; it never refuses an edit, and a name that is itself
// known (in use, in the cache, on the list) is never flagged, however
// close it is to another — react and preact are both real.
package typosquat

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kinds of resemblance, as a finding's line names them.
const (
	KindCase      = "differs only in letter case from"
	KindSeparator = "differs only in - _ . from"
	KindLookalike = "uses lookalike characters for"
	KindEdit      = "is one keystroke from"
	KindAffix     = "decorates"
	KindNonASCII  = "has non-ASCII letters"
)

// minEditLen is the shortest name the one-keystroke rule applies to:
// below it, one edit away is most of the alphabet (ms, mx, ws…).
const minEditLen = 5

// Resemble reports how name resembles known, "" for not at all. name and
// known are compared as given; a caller normalises first where the
// ecosystem says case or separators do not matter.
func Resemble(name, known string) string {
	if name == known || name == "" || known == "" {
		return ""
	}
	if strings.EqualFold(name, known) {
		return KindCase
	}
	if stripSeparators(strings.ToLower(name)) == stripSeparators(strings.ToLower(known)) {
		return KindSeparator
	}
	if skeleton(name) == skeleton(known) {
		return KindLookalike
	}
	if affixed(strings.ToLower(name), strings.ToLower(known)) {
		return KindAffix
	}
	if utf8.RuneCountInString(known) >= minEditLen && osa(strings.ToLower(name), strings.ToLower(known)) == 1 {
		return KindEdit
	}
	return ""
}

func stripSeparators(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' {
			return -1
		}
		return r
	}, s)
}

// confusables maps characters that render like a Latin letter or digit
// to the one they pass for. It is the short list that matters for package
// names: Cyrillic and Greek lookalikes, and the ASCII pairs a quick read
// confuses (l, I, 1; O, 0; rn, m; vv, w).
var confusables = map[rune]string{
	'а': "a", 'е': "e", 'о': "o", 'р': "p", 'с': "c", 'у': "y", 'х': "x", 'і': "l", 'ј': "j", 'ѕ': "s",
	'ԁ': "d", 'ɡ': "g", 'һ': "h", 'ӏ': "l", 'ո': "n", 'ս': "u", 'ν': "v", 'ο': "o", 'α': "a", 'ρ': "p",
	'τ': "t", 'κ': "k", 'ι': "l", 'Α': "a", 'Β': "b", 'Ε': "e", 'Η': "h", 'Ι': "l", 'Κ': "k", 'Μ': "m",
	'Ν': "n", 'Ο': "o", 'Ρ': "p", 'Τ': "t", 'Χ': "x", 'Υ': "y", 'Ζ': "z",
	'0': "o", '1': "l", 'i': "l", 'I': "l", '|': "l", '!': "l",
}

// skeleton is the string a name reads as at a glance: lower-cased, its
// lookalike characters folded onto one representative, its separators
// gone. Two different names with one skeleton are a lookalike pair.
func skeleton(s string) string {
	var b strings.Builder
	for _, r := range s {
		if m, ok := confusables[r]; ok {
			b.WriteString(m)
			continue
		}
		if r == '-' || r == '_' || r == '.' {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	out := b.String()
	out = strings.ReplaceAll(out, "rn", "m")
	out = strings.ReplaceAll(out, "vv", "w")
	return out
}

// decorations are what a squatter bolts onto a real name: the language,
// the runtime, a "js" the real package never had.
var decorations = []string{"python3", "python", "py", "node", "nodejs", "js", "go", "golang", "lib", "cli", "dev", "official"}

// affixed reports whether name is known with a decoration added, or
// with its decoration swapped for another (python3-dateutil for
// python-dateutil).
func affixed(name, known string) bool {
	nc, kc := core(name), core(known)
	if nc == kc && nc != "" && len(nc) >= 3 && (nc != name || kc != known) {
		return true
	}
	return false
}

// core is s with one decoration and its separator taken off either end.
func core(s string) string {
	for _, d := range decorations {
		for _, sep := range []string{"-", "_", ".", ""} {
			if p := d + sep; sep != "" && strings.HasPrefix(s, p) && len(s) > len(p) {
				return strings.TrimLeft(s[len(p):], "-_.")
			}
			if p := sep + d; strings.HasSuffix(s, p) && len(s) > len(p) && (sep != "" || len(s)-len(p) >= 4) {
				return strings.TrimRight(s[:len(s)-len(p)], "-_.")
			}
		}
	}
	return s
}

// osa is the optimal-string-alignment distance: insertions, deletions,
// substitutions and adjacent transpositions, each costing one.
func osa(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > 2 || d < -2 {
		return 3 // further than any rule here cares about
	}
	prev2 := make([]int, len(rb)+1)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(rb)]
}

// nonASCII reports a name with a letter outside ASCII. No registry this
// package knows serves one, so it is worth a line on its own.
func nonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return true
		}
	}
	return false
}
