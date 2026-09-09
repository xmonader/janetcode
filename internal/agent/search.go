package agent

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/xmonader/janetcode/internal/tool"
)

// Ranked search over the archive. A model recovering an exact value it no
// longer has (a path, an error line, a number) does not know the regexp
// that finds it; it knows roughly what it was about. A grep that demands
// every word match returns nothing for such queries — a retriever with
// exactly that AND semantics scored 0% on model-written questions until
// it was made to rank by any-term overlap. This one scores every window
// of lines by BM25 over the query's words, gives literal anchors (paths,
// identifiers, numbers) an exact-match boost, expands each hit to its
// neighbouring lines, and returns the best windows within a token budget.

const (
	searchWindow  = 6     // lines per scored window
	searchStride  = 3     // windows overlap by half
	searchContext = 2     // extra lines shown around a hit
	searchBudget  = 6_000 // bytes of output, about 1.5k tokens
	searchTopK    = 12
	bm25K1        = 1.2
	bm25B         = 0.75
)

var (
	tokenRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_./:-]*`)
	partRe  = regexp.MustCompile(`[A-Za-z]+|[0-9]+`)
)

// terms splits text into lowercase search terms. A literal such as
// internal/server/router.go:42 is indexed whole (so an exact mention
// anchors) and by its parts (so "router" finds it), and words are lightly
// stemmed so "compiling" meets "compile".
func terms(s string) []string {
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
			if p = stem(p); len(p) >= 2 && !stopword[p] {
				out = append(out, p)
			}
		}
	}
	return out
}

// stem trims the commonest English suffixes; enough to match a query's
// "compile" or "fail" against "compiling" and "failed" without a
// dictionary, and cheap enough to run on every archived line.
func stem(w string) string {
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

// anchor reports whether a term is a literal worth an exact-match boost:
// something a model would not paraphrase, like a path, an identifier
// with punctuation, or a number.
func anchor(t string) bool {
	if strings.ContainsAny(t, "_./:-") {
		return true
	}
	digits := 0
	for _, r := range t {
		if unicode.IsDigit(r) {
			digits++
		}
	}
	return digits > 0 && digits >= len(t)/2
}

var (
	lineRe = regexp.MustCompile(`(?i)\b(?:line|:)\s*(\d{1,6})\b`)
	pathRe = regexp.MustCompile(`[\w./-]*[\w-]+\.[A-Za-z][\w]{0,8}\b`)
	// numbered matches a read_file result's "12\tcode" line prefix.
	numbered = regexp.MustCompile(`(?m)^(\d+)\t`)
)

// lineLookup answers "line N of <file>" from the archive. Tool output that
// numbers its lines — read_file's does — carries the file's own numbering
// into the archive, so the line can be found exactly rather than guessed
// at by word overlap. It returns "" when the query is not of that shape or
// nothing matches, and the caller falls back to ranking.
func lineLookup(ar *Archive, ids []string, query string) string {
	lm := lineRe.FindStringSubmatch(query)
	if lm == nil {
		return ""
	}
	want, err := strconv.Atoi(lm[1])
	if err != nil || want <= 0 {
		return ""
	}
	path := ""
	for _, cand := range pathRe.FindAllString(query, -1) {
		if strings.Contains(cand, "/") || strings.Contains(cand, ".") {
			path = cand
			break
		}
	}
	if path == "" {
		return ""
	}
	entries := entriesFor(ar, ids)
	var b strings.Builder
	for _, e := range entries {
		// The entry must be about this file: either the call's subject is
		// the path, or the archived text names it (a compacted
		// conversation holds many files' reads).
		_, body, err := ar.Get(e.ID)
		if err != nil {
			continue
		}
		subjectMatch := e.Subject != "" && (strings.HasSuffix(e.Subject, path) || strings.HasSuffix(path, e.Subject))
		if !subjectMatch && !strings.Contains(body, path) {
			continue
		}
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		// Inside a compacted conversation the file's read starts after the
		// line naming it; restrict to the nearest such block so line 36 of
		// this file is not confused with line 36 of another.
		from, to := blockFor(lines, path, subjectMatch)
		for i := from; i < to; i++ {
			m := numbered.FindStringSubmatch(lines[i] + "\n")
			if m == nil || m[1] != lm[1] {
				continue
			}
			lo, hi := max(i-2, from), min(i+3, to)
			fmt.Fprintf(&b, "%s (%s) — line %d of %s:\n", e.ID, e.Name, want, path)
			for j := lo; j < hi; j++ {
				marker := "  "
				if j == i {
					marker = "> "
				}
				fmt.Fprintf(&b, "%s%s\n", marker, lines[j])
			}
			b.WriteString("\n")
			break
		}
		if b.Len() > 0 {
			break
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}

// blockFor narrows a body to the part about path. When the entry is that
// file's own output the whole body is it; inside a compacted conversation
// it is the span from the mention of the path to the next one.
func blockFor(lines []string, path string, subjectMatch bool) (int, int) {
	if subjectMatch {
		return 0, len(lines)
	}
	start := -1
	for i, l := range lines {
		if strings.Contains(l, path) {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, len(lines)
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		// The next tool call ends this file's output.
		if strings.HasPrefix(lines[i], "[tool call ") || strings.HasPrefix(lines[i], "=== message ") {
			end = i
			break
		}
	}
	return start, end
}

// entriesFor is the entries to search: the named ones, or all of them.
func entriesFor(ar *Archive, ids []string) []Entry {
	if len(ids) == 0 {
		return ar.List()
	}
	var out []Entry
	for _, id := range ids {
		if e, _, err := ar.Get(id); err == nil {
			out = append(out, e)
		}
	}
	return out
}

type window struct {
	entry   string // archive id
	name    string
	subject string
	start   int // first line index
	lines   []string
	score   float64
}

// searchArchive ranks the windows of the given entries (all when ids is
// empty) against query and renders the best within the budget.
func searchArchive(ar *Archive, ids []string, query string, budget int) string {
	if budget <= 0 {
		budget = searchBudget
	}
	// "line 36 of ledger/parse.py" is the commonest thing to want back
	// after a compaction, and it is the one question ranking by words
	// cannot answer: the query shares no vocabulary with the line. When a
	// query names a file and a line, the line is looked up directly.
	if out := lineLookup(ar, ids, query); out != "" {
		return out
	}
	q := terms(query)
	if len(q) == 0 {
		return "The query has no searchable words."
	}
	entries := entriesFor(ar, ids)
	var wins []window
	df := map[string]int{}
	totalLen := 0
	for _, e := range entries {
		_, body, err := ar.Get(e.ID)
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		for start := 0; start < len(lines); start += searchStride {
			end := min(start+searchWindow, len(lines))
			w := window{entry: e.ID, name: e.Name, subject: e.Subject, start: start, lines: lines[start:end]}
			seen := map[string]bool{}
			n := 0
			for _, l := range w.lines {
				for _, t := range terms(l) {
					n++
					if !seen[t] {
						seen[t] = true
						df[t]++
					}
				}
			}
			totalLen += n
			wins = append(wins, w)
			if end == len(lines) {
				break
			}
		}
	}
	if len(wins) == 0 {
		return "Nothing is archived to search."
	}
	avg := float64(totalLen) / float64(len(wins))
	for i := range wins {
		w := &wins[i]
		tf := map[string]int{}
		n := 0
		for _, l := range w.lines {
			for _, t := range terms(l) {
				tf[t]++
				n++
			}
		}
		text := strings.ToLower(strings.Join(w.lines, "\n"))
		// A query naming what the entry is about — its path, its command —
		// is the strongest signal there is: the model remembers which file
		// it read, not what line 63 said. Every window of that entry gets
		// the anchor bonus for each such term.
		subj := strings.ToLower(w.subject)
		for _, t := range q {
			if subj != "" && anchor(t) && strings.Contains(subj, t) {
				w.score += 3
			}
		}
		for _, t := range q {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(wins))-float64(df[t])+0.5)/(float64(df[t])+0.5))
			w.score += idf * (f * (bm25K1 + 1)) / (f + bm25K1*(1-bm25B+bm25B*float64(n)/math.Max(avg, 1)))
			if anchor(t) && strings.Contains(text, t) {
				w.score += idf // an exact literal counts twice
			}
		}
	}
	sort.SliceStable(wins, func(i, j int) bool { return wins[i].score > wins[j].score })
	var sb strings.Builder
	fmt.Fprintf(&sb, "Best matches for %q, ranked:\n", query)
	shown := 0
	covered := map[string]bool{} // entry:start already printed
	for _, w := range wins {
		if w.score <= 0 || shown >= searchTopK || sb.Len() >= budget {
			break
		}
		key := fmt.Sprintf("%s:%d", w.entry, w.start/searchStride/2)
		if covered[key] {
			continue
		}
		covered[key] = true
		_, body, err := ar.Get(w.entry)
		if err != nil {
			continue
		}
		all := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
		from := max(w.start-searchContext, 0)
		to := min(w.start+len(w.lines)+searchContext, len(all))
		label := w.name
		if w.subject != "" {
			label += " " + w.subject
		}
		fmt.Fprintf(&sb, "\n%s (%s) lines %d-%d:\n", w.entry, label, from+1, to)
		for i := from; i < to; i++ {
			fmt.Fprintf(&sb, "%d\t%s\n", i+1, all[i])
		}
		shown++
	}
	if shown == 0 {
		return fmt.Sprintf("No archived output mentions any of: %s.", strings.Join(q, ", "))
	}
	if sb.Len() > budget {
		return tool.Truncate(sb.String()[:budget]) + "\n[more matches not shown; narrow the query or give an id]"
	}
	return sb.String()
}
