// Package terms splits text into the search terms a BM25 ranking scores
// over, with the stemming and stop-word list that ranking assumes. It is
// its own package because two rankings depend on the same splitting —
// internal/agent's archive search and internal/undoabout's keyword
// fallback — and a term split one way in one and another way in the other
// would make the two disagree about what a word is.
package terms

import (
	"regexp"
	"strings"
)

// K1 and B are the BM25 parameters both rankings use.
const (
	K1 = 1.2
	B  = 0.75
)

var (
	tokenRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./:-]*`)
	partRe  = regexp.MustCompile(`[A-Za-z]+|[0-9]+`)
)

// Split splits text into lowercase search terms. A literal such as
// internal/server/router.go:42 is indexed whole (so an exact mention
// anchors) and by its parts (so "router" finds it), and words are lightly
// stemmed so "compiling" meets "compile".
func Split(s string) []string {
	var out []string
	for _, t := range tokenRe.FindAllString(s, -1) {
		t = strings.ToLower(strings.Trim(t, "./:-"))
		if len(t) < 2 || stopword[t] {
			continue
		}
		parts := partRe.FindAllString(t, -1)
		if len(parts) > 1 {
			out = append(out, t)
		}
		for _, p := range parts {
			if p = Stem(p); len(p) >= 2 && !stopword[p] {
				out = append(out, p)
			}
		}
	}
	return out
}

// Stem trims the commonest English suffixes; enough to match a query's
// "compile" or "fail" against "compiling" and "failed" without a
// dictionary, and cheap enough to run on every archived line.
func Stem(w string) string {
	for _, suf := range []string{"ing", "ies", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			w = strings.TrimSuffix(w, suf)
			if suf == "ies" {
				w += "y"
			}
			break
		}
	}
	return w
}

var stopword = map[string]bool{"the": true, "a": true, "an": true, "of": true, "in": true, "to": true, "was": true, "what": true, "is": true, "did": true, "and": true, "or": true, "it": true, "that": true, "for": true, "on": true, "with": true, "this": true, "be": true, "by": true, "at": true, "from": true, "as": true}
