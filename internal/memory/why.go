package memory

import (
	"fmt"
	"strings"
)

// Match is why an entry scored as it did against a query: the query words
// that hit its name or description (three points each), those that hit its
// body (one each) and the path or symbol segments of Concerns that the
// query names (two each, at most two).
type Match struct {
	Title, Body, Concerns []string
	Score                 int
}

// scoreEntry is the lexical relevance of e to the query words in have. It
// is the one scoring rule: rankPool admits and orders by its score, and Why
// reports the same Match, so an explanation cannot drift from the ranking
// it explains.
func scoreEntry(e Entry, have map[string]bool) (int, Match) {
	var m Match
	seen := map[string]bool{}
	for _, w := range Words(e.Name + " " + e.Description) {
		if have[w] && !seen[w] && !stop[w] {
			seen[w] = true
			m.Title = append(m.Title, w)
		}
	}
	for _, w := range Words(e.Body) {
		if have[w] && !seen[w] && !stop[w] {
			seen[w] = true
			m.Body = append(m.Body, w)
		}
	}
	// A project entry about a path or symbol the turn names is relevant
	// through the work itself, not only through words: two matched
	// segments admit it, one adds to whatever words matched.
	for _, c := range e.Concerns {
		for _, w := range Words(strings.ReplaceAll(c, "/", " ")) {
			if have[w] && !stop[w] && !seen[w] {
				seen[w] = true
				m.Concerns = append(m.Concerns, w)
			}
		}
	}
	concerns := len(m.Concerns)
	if concerns > 2 {
		concerns = 2
	}
	m.Score = 3*len(m.Title) + len(m.Body) + 2*concerns
	return m.Score, m
}

// Why says in one line why e was attached to a request about text: the
// words it matched and where. An entry the word match does not explain was
// found by meaning (embedding similarity), and says so.
func (s *Store) Why(text string, e Entry) string {
	have := map[string]bool{}
	for _, w := range s.words(text) {
		have[w] = true
	}
	_, m := scoreEntry(e, have)
	var parts []string
	if len(m.Title) > 0 {
		parts = append(parts, "name/description: "+strings.Join(m.Title, ", "))
	}
	if len(m.Body) > 0 {
		parts = append(parts, "body: "+strings.Join(m.Body, ", "))
	}
	if len(m.Concerns) > 0 {
		parts = append(parts, "concerns: "+strings.Join(m.Concerns, ", "))
	}
	if len(parts) == 0 {
		return "similar in meaning to the request (embedding match, no shared words)"
	}
	return fmt.Sprintf("matched %s (score %d)", strings.Join(parts, "; "), m.Score)
}
