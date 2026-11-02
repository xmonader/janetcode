package memory

import (
	"regexp"
	"strings"
	"unicode"
)

// Retrieval-quality helpers ported from grok's xai-grok-memory (mmr.rs,
// query_expansion.rs, chunker.rs). All three are deterministic and make no
// model call: MMR works on word-set Jaccard, expansion on string rules,
// chunking on markdown structure. Each has a Store gate (NoMMR, NoExpand,
// NoChunk) wired to a JANETCODE_OFF switch (mmr, queryexpand, memchunk).

// MMRLambda weighs relevance against redundancy: 1 is plain ranking, 0
// is pure diversity.
const MMRLambda = 0.7

// mmrPool is how many candidates per slot MMR may choose from.
const mmrPool = 3

// diversify picks up to max entries from candidates (best first, rel
// aligned and descending) by Maximal Marginal Relevance:
// lambda*relevance - (1-lambda)*maxJaccard(candidate, picked). The first
// pick is always the most relevant, so a lone good match is unchanged.
func diversify(es []Entry, rel []float64, max int, lambda float64) []Entry {
	if max <= 0 || len(es) == 0 {
		return nil
	}
	if len(es) <= 1 || lambda >= 1 {
		if len(es) > max {
			es = es[:max]
		}
		return es
	}
	sets := make([]map[string]bool, len(es))
	for i, e := range es {
		sets[i] = wordSet(e.Name + " " + e.Description + " " + e.Body)
	}
	lo, hi := rel[0], rel[0]
	for _, r := range rel {
		if r < lo {
			lo = r
		}
		if r > hi {
			hi = r
		}
	}
	span := hi - lo
	if span <= 0 {
		span = 1
	}
	var picked []int
	left := make([]int, len(es))
	for i := range left {
		left[i] = i
	}
	for len(picked) < max && len(left) > 0 {
		bestPos, best := 0, -1e18
		for pos, c := range left {
			sim := 0.0
			for _, p := range picked {
				if j := jaccard(sets[c], sets[p]); j > sim {
					sim = j
				}
			}
			m := lambda*(rel[c]-lo)/span - (1-lambda)*sim
			if m > best { // ties keep the earlier, more relevant, candidate
				best, bestPos = m, pos
			}
		}
		picked = append(picked, left[bestPos])
		left = append(left[:bestPos], left[bestPos+1:]...)
	}
	out := make([]Entry, len(picked))
	for i, p := range picked {
		out[i] = es[p]
	}
	return out
}

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range Words(s) {
		if !stop[w] {
			m[w] = true
		}
	}
	return m
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	both := 0
	for w := range a {
		if b[w] {
			both++
		}
	}
	return float64(both) / float64(len(a)+len(b)-both)
}

// chatter is conversational filler that carries no topic: grok's
// query-expansion stop list, minus what janetcode's own stop list has.
var chatter = map[string]bool{
	"thing": true, "stuff": true, "something": true, "anything": true, "everything": true,
	"some": true, "each": true, "every": true, "both": true, "few": true, "more": true,
	"yesterday": true, "today": true, "tomorrow": true, "earlier": true, "later": true,
	"recently": true, "now": true, "remember": true, "discussed": true, "talked": true,
	"about": true, "was": true, "were": true, "have": true, "has": true, "had": true,
	"did": true, "does": true, "would": true, "could": true, "where": true, "which": true,
	"who": true, "why": true, "then": true, "because": true, "while": true, "tell": true,
	"show": true, "find": true, "help": true, "get": true, "let": true, "know": true,
}

var identRe = regexp.MustCompile(`[A-Za-z0-9_]+`)

// ExpandQuery turns a conversational query into the words to match: the
// query's own words minus filler ("that thing we discussed about ..."),
// plus the parts of every identifier in it (fooBarBaz, foo_bar,
// pkg.Func), so a turn that names parseConfig finds a note about
// "config parsing". Pure string rules; no model call.
func ExpandQuery(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(w string) {
		if w != "" && !seen[w] && !chatter[w] && !stop[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	for _, w := range Words(text) {
		add(w)
	}
	for _, tok := range identRe.FindAllString(text, -1) {
		parts := splitIdent(tok)
		if len(parts) < 2 {
			continue
		}
		for _, p := range parts {
			for _, w := range Words(p) {
				add(w)
			}
		}
	}
	return out
}

// splitIdent splits camelCase, PascalCase and snake_case into parts.
func splitIdent(s string) []string {
	var parts []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			parts = append(parts, string(cur))
			cur = nil
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_':
			flush()
		case unicode.IsUpper(r) && len(cur) > 0 &&
			(unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]) ||
				(i+1 < len(rs) && unicode.IsLower(rs[i+1]))):
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	return parts
}

// Chunk limits, in bytes (about four per token).
const (
	ChunkSize    = 800
	chunkOverlap = 120
)

// Chunk splits text for embedding: on markdown headings and blank lines,
// packing paragraphs up to size bytes, every chunk after the first
// carrying the nearest heading above it so it stands alone, and a
// paragraph longer than size split on line then rune boundaries with a
// small overlap. Text that fits comes back whole.
func Chunk(text string, size int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if size <= 0 {
		size = ChunkSize
	}
	if len(text) <= size {
		return []string{text}
	}
	type para struct{ head, text string }
	var paras []para
	head := ""
	for _, p := range strings.Split(text, "\n\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "#") {
			head = firstLine(p)
		}
		paras = append(paras, para{head, p})
	}
	var out []string
	var cur strings.Builder
	curHead := ""
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
	}
	for _, p := range paras {
		pieces := []string{p.text}
		if len(p.text) > size {
			pieces = splitLong(p.text, size)
		}
		for _, piece := range pieces {
			if cur.Len() > 0 && cur.Len()+len(piece)+2 > size {
				flush()
			}
			if cur.Len() == 0 && len(out) > 0 && p.head != "" && !strings.HasPrefix(piece, p.head) && len(piece)+len(p.head)+1 <= size {
				cur.WriteString(p.head + "\n")
			}
			if cur.Len() > 0 {
				cur.WriteString("\n")
			}
			cur.WriteString(piece)
			curHead = p.head
		}
	}
	_ = curHead
	flush()
	return out
}

// splitLong cuts s into pieces of at most size bytes, preferring line
// breaks, with chunkOverlap bytes repeated at each seam.
func splitLong(s string, size int) []string {
	var out []string
	for len(s) > size {
		cut := size
		if i := strings.LastIndex(s[:cut], "\n"); i > size/2 {
			cut = i
		}
		for cut > 0 && !validStart(s[cut]) {
			cut--
		}
		out = append(out, strings.TrimSpace(s[:cut]))
		next := cut - chunkOverlap
		if next <= 0 {
			next = cut
		}
		for next < len(s) && !validStart(s[next]) {
			next++
		}
		s = s[next:]
	}
	if strings.TrimSpace(s) != "" {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// validStart reports whether b can begin a UTF-8 sequence.
func validStart(b byte) bool { return b&0xC0 != 0x80 }
