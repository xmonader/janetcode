package agent

import (
	"regexp"
	"sort"
	"strings"
)

// Edge rules of a brief that the turn's own tests never assert at their
// edge. Zero model calls: string work over the brief and the turn's
// checks, used by edgerules_note.go. hero_agent's lean coding turns missed
// explicit edge rules that pi got (effbench 2026-10-08, c-rs-logstore
// invalid_header_is_corrupt_even_if_short on three models; cronexpr's
// "error: " prefix; ties "also when descending"): each time the model HAD
// tests, of the neighbouring case. A test named for "torn tail" makes a
// rule look covered, so a rule counts as checked only when ONE assertion
// names the rule (two of its terms) AND its edge: the literal of an error
// format, the words after "even if", a word of the previous step of an
// ordered list, a boundary word for a threshold or "strictly". It errs
// toward "unchecked": a false "unchecked" costs a short look at a rule
// already tested, a false "checked" costs the pass rate. "None means
// absent" and "ignored" rules are left out as in hero_agent: they made
// the note fire on 81% of passing runs (59% without). Simplified from
// hero_agent's rule_checks: sections of a brief that report or test
// are skipped by heading and a few sentence filters, not its full set.

type edgeKind int

// Declared in the order a note prefers them (hero_agent: the kinds behind
// the most recorded misses first).
const (
	edgeOrder edgeKind = iota
	edgeThreshold
	edgeStrict
	edgeMessage
	edgeEvenIf
)

type edgeRule struct {
	text    string
	kind    edgeKind
	after   string // for edgeOrder: the step before
	key     map[string]bool
	witness map[string]bool
	literal string
}

var (
	evenIfMarks = []string{"even if", "even when", "even though", "also when", "also if", "regardless of", "whether or not"}
	orderLeads  = []string{"in this order", "in the following order", "in order:"}
	decideWords = []string{"decide", "check", "validat", "evaluat", "first"}
	thrBefore   = []string{"fewer than", "less than", "more than", "greater than", "longer than", "shorter than", "at least", "at most", "no more than", "no fewer than", "up to"}
	thrAfter    = []string{"or more", "or fewer", "or less", "or longer", "or shorter"}
	boundaryW   = []string{"short", "shorter", "fewer", "boundary", "edge", "threshold", "tiny", "minimum", "maximum"}
	strictW     = []string{"strict", "strictly", "equal", "same", "exact", "exactly", "boundary", "edge", "itself"}
	// rareInBrief: a term in more sentences than this is the brief's
	// vocabulary, not what tells one ordered step's edge from another.
	rareInBrief = 2
)

var stopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a an and are as at be been but by can do does for from has have if in into is it its not of on or so than that the their then there these this to was were when which will with would should must may also each any all only no one two more less than then both`) {
		m[w] = true
	}
	return m
}()

func stemWord(word string) string {
	w := strings.ToLower(word)
	for _, suf := range []string{"ing", "ed", "es", "s", "e"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			w = w[:len(w)-len(suf)]
			break
		}
	}
	if n := len(w); n >= 4 && w[n-1] == w[n-2] && !strings.ContainsRune("aeiou", rune(w[n-1])) {
		w = w[:n-1]
	}
	if len(w) > 7 {
		w = w[:7]
	}
	return w
}

// wordsOf splits on non-alphanumerics and camelCase.
func wordsOf(text string) []string {
	var out []string
	var cur []rune
	prevLower := false
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for _, c := range text {
		alnum := c < 128 && (c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z')
		if !alnum {
			prevLower = false
			flush()
			continue
		}
		if c >= 'A' && c <= 'Z' && prevLower {
			flush()
		}
		prevLower = c >= 'a' && c <= 'z'
		cur = append(cur, c)
	}
	flush()
	return out
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// termsOf: the content terms of text, stemmed.
func termsOf(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordsOf(text) {
		if len(w) > 1 && !allDigits(w) && !stopWords[strings.ToLower(w)] {
			out[stemWord(w)] = true
		}
	}
	return out
}

func rawStems(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordsOf(text) {
		out[stemWord(w)] = true
	}
	return out
}

func stemsOfList(l []string) map[string]bool {
	out := map[string]bool{}
	for _, w := range l {
		out[stemWord(w)] = true
	}
	return out
}

func sameWord(a, b string) bool {
	return a == b || (min(len(a), len(b)) >= 4 && (strings.HasPrefix(a, b) || strings.HasPrefix(b, a)))
}

func hitCount(set, unit map[string]bool) int {
	n := 0
	for t := range set {
		for u := range unit {
			if sameWord(t, u) {
				n++
				break
			}
		}
	}
	return n
}

// clauseAfter: the text after the first marker, up to the clause end.
func clauseAfter(lower, marker string) (string, bool) {
	i := strings.Index(lower, marker)
	if i < 0 {
		return "", false
	}
	rest := lower[i+len(marker):]
	if j := strings.IndexAny(rest, ",;.):—"); j >= 0 {
		rest = rest[:j]
	}
	return rest, true
}

// errorLiteral: the `error: ...` code span of a sentence, cut before its
// first placeholder (MESSAGE, TEXT).
func errorLiteral(sentence string) string {
	parts := strings.Split(sentence, "`")
	for i := 1; i < len(parts); i += 2 {
		span := parts[i]
		if !strings.HasPrefix(strings.ToLower(span), "error:") {
			continue
		}
		if j := strings.IndexAny(span, "<${"); j >= 0 {
			span = span[:j]
		}
		var lit strings.Builder
		for _, word := range strings.SplitAfter(span, " ") {
			bare := strings.Trim(strings.TrimSpace(word), "!\"'.,;:()[]")
			if len(bare) >= 3 && strings.ToUpper(bare) == bare && strings.Trim(bare, "ABCDEFGHIJKLMNOPQRSTUVWXYZ_") == "" {
				break
			}
			lit.WriteString(word)
		}
		if s := strings.TrimRight(lit.String(), " "); len(s) >= len("error:") {
			return s
		}
	}
	return ""
}

var numRe = regexp.MustCompile(`^[0-9]+$`)

func hasThreshold(lower string) bool {
	toks := strings.FieldsFunc(lower, func(r rune) bool { return r == ' ' || r == '\t' || r == '`' })
	joined := strings.Join(toks, " ")
	isNum := func(t string) bool { return numRe.MatchString(strings.Trim(t, ".,;:()")) }
	for _, p := range thrBefore {
		for off := 0; ; {
			i := strings.Index(joined[off:], p)
			if i < 0 {
				break
			}
			rest := strings.Fields(joined[off+i+len(p):])
			if len(rest) >= 1 && isNum(rest[0]) || len(rest) >= 2 && isNum(rest[1]) {
				return true
			}
			off += i + len(p)
		}
	}
	for _, p := range thrAfter {
		for off := 0; ; {
			i := strings.Index(joined[off:], p)
			if i < 0 {
				break
			}
			if before := strings.Fields(joined[:off+i]); len(before) > 0 && isNum(before[len(before)-1]) {
				return true
			}
			off += i + len(p)
		}
	}
	return false
}

func hasPhrase(lower, phrase string) bool {
	for off := 0; ; {
		i := strings.Index(lower[off:], phrase)
		if i < 0 {
			return false
		}
		at := off + i
		okB := at == 0 || !isAlnumByte(lower[at-1])
		okA := at+len(phrase) >= len(lower) || !isAlnumByte(lower[at+len(phrase)])
		if okB && okA {
			return true
		}
		off = at + len(phrase)
	}
}

func isAlnumByte(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

type briefBlock struct {
	text     string
	numbered bool
}

var listRe = regexp.MustCompile(`^(?:[-*+]|[0-9]+[.)])\s+(.*)$`)

// briefBlocks: paragraphs and list items of the brief, without code
// fences, tables, quotes, and the sections that report or test.
func briefBlocks(spec string) []briefBlock {
	var out []briefBlock
	var cur *string
	numbered, fenced, skipping := false, false, false
	flush := func() {
		if cur != nil {
			out = append(out, briefBlock{*cur, numbered})
			cur = nil
		}
	}
	for _, raw := range strings.Split(spec, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			flush()
			continue
		}
		if fenced {
			continue
		}
		if strings.HasPrefix(line, "#") {
			flush()
			h := strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
			skipping = false
			for _, s := range []string{"public tests", "tests", "done when", "definition of done", "deliverables", "grading", "report", "example"} {
				if strings.HasPrefix(h, s) {
					skipping = true
				}
			}
			continue
		}
		if skipping {
			continue
		}
		if line == "" || strings.HasPrefix(line, "|") || strings.HasPrefix(line, ">") {
			flush()
			continue
		}
		if m := listRe.FindStringSubmatch(line); m != nil {
			flush()
			s := m[1]
			cur = &s
			numbered = line[0] >= '0' && line[0] <= '9'
			continue
		}
		if cur == nil {
			s := line
			cur = &s
			numbered = false
		} else {
			*cur += " " + line
		}
	}
	flush()
	return out
}

var sentenceRe = regexp.MustCompile(`[.!?]\s+`)

func sentencesOf(text string) []string {
	var out []string
	last := 0
	for _, m := range sentenceRe.FindAllStringIndex(text, -1) {
		if s := strings.TrimSpace(text[last : m[0]+1]); s != "" {
			out = append(out, s)
		}
		last = m[1]
	}
	if s := strings.TrimSpace(text[last:]); s != "" {
		out = append(out, s)
	}
	return out
}

func notARule(sentence string) bool {
	l := strings.ToLower(sentence)
	return strings.HasPrefix(l, "example") || strings.HasPrefix(l, "for example") || strings.HasPrefix(l, "e.g.") ||
		strings.Contains(l, "up to you") || strings.Contains(l, "public test") || strings.Contains(l, "test_public") ||
		strings.Contains(l, "std-only") || strings.Contains(l, " is our ")
}

func containsAny(s string, l []string) bool {
	for _, x := range l {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// edgeRules: the brief's edge rules, in order.
func edgeRules(spec string) []edgeRule {
	blocks := briefBlocks(spec)
	df := map[string]int{}
	for _, b := range blocks {
		for _, s := range sentencesOf(b.text) {
			for t := range termsOf(s) {
				df[t]++
			}
		}
	}
	var out []edgeRule
	ordered := false
	prevStep := ""
	for _, b := range blocks {
		if !b.numbered {
			l := strings.ToLower(b.text)
			ordered = containsAny(l, orderLeads) && containsAny(l, decideWords)
			prevStep = ""
		}
		for _, s := range sentencesOf(b.text) {
			lower := strings.ToLower(s)
			key := termsOf(s)
			isRule := !notARule(s) && len(key) >= 3
			stepBefore := ""
			if b.numbered && ordered {
				stepBefore = prevStep
				prevStep = s
			}
			if !isRule {
				continue
			}
			r := edgeRule{text: s, kind: edgeEvenIf, key: key, witness: map[string]bool{}}
			cond := ""
			condOK := false
			for _, m := range evenIfMarks {
				if c, ok := clauseAfter(lower, m); ok {
					cond, condOK = c, true
					break
				}
			}
			switch {
			case errorLiteral(s) != "":
				r.kind, r.literal = edgeMessage, errorLiteral(s)
			case condOK:
				r.witness = termsOf(cond)
			case stepBefore != "":
				r.kind, r.after = edgeOrder, stepBefore
				for t := range termsOf(stepBefore) {
					if !key[t] && df[t] <= rareInBrief {
						r.witness[t] = true
					}
				}
				if hasThreshold(strings.ToLower(stepBefore)) {
					for t := range stemsOfList(boundaryW) {
						r.witness[t] = true
					}
				}
			case hasThreshold(lower):
				r.kind, r.witness = edgeThreshold, stemsOfList(boundaryW)
			case hasPhrase(lower, "strictly"):
				r.kind, r.witness = edgeStrict, stemsOfList(strictW)
			default:
				continue
			}
			if r.kind != edgeMessage && len(r.witness) == 0 {
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

var assertRe = regexp.MustCompile(`(?i)\b(assert\w*|expect\w*|require|check|should|verify|raises|equal\w*|t\.(Error|Fatal)\w*|if\s.*!=|print|echo|grep|diff|test)\b`)
var funcDeclRe = regexp.MustCompile(`(?:func|def|fn|it|test)\s*\(?\s*["']?(\w[\w ]*)`)

// checkUnits: the assertion lines of a check script or test file, each
// with the name of the function it sits in.
func checkUnits(script string) []string {
	var out []string
	fn := ""
	for _, line := range strings.Split(script, "\n") {
		if m := funcDeclRe.FindStringSubmatch(line); m != nil && !strings.Contains(line, "assert") {
			fn = m[1]
			continue
		}
		if assertRe.MatchString(line) {
			out = append(out, fn+" "+strings.TrimSpace(line))
		}
	}
	return out
}

func reaches(r edgeRule, unit string) bool {
	if hitCount(r.key, termsOf(unit)) < min(len(r.key), 2) {
		return false
	}
	if r.kind == edgeMessage {
		return r.literal != "" && strings.Contains(unit, r.literal)
	}
	return hitCount(r.witness, rawStems(unit)) > 0
}

// uncheckedEdgeRules: the edge rules no assertion in checks reaches.
func uncheckedEdgeRules(spec string, checks []string) []edgeRule {
	var units []string
	for _, c := range checks {
		units = append(units, checkUnits(c)...)
	}
	var out []edgeRule
next:
	for _, r := range edgeRules(spec) {
		for _, u := range units {
			if reaches(r, u) {
				continue next
			}
		}
		out = append(out, r)
	}
	return out
}

// pickEdgeRules: at most max, in brief order: the first rule of each
// kind (kinds in declared order), then the rest.
func pickEdgeRules(un []edgeRule, max int) []edgeRule {
	seen := map[edgeKind]bool{}
	var first []int
	for i, r := range un {
		if !seen[r.kind] {
			seen[r.kind] = true
			first = append(first, i)
		}
	}
	sort.SliceStable(first, func(a, b int) bool { return un[first[a]].kind < un[first[b]].kind })
	chosen := map[int]bool{}
	for _, i := range first {
		if len(chosen) < max {
			chosen[i] = true
		}
	}
	for i := range un {
		if len(chosen) < max && !chosen[i] {
			chosen[i] = true
		}
	}
	var idx []int
	for i := range chosen {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out []edgeRule
	for _, i := range idx {
		out = append(out, un[i])
	}
	return out
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (r edgeRule) short(max int) string {
	if r.after != "" {
		return cutRunes(r.text, max*2/3) + " (decided after: " + cutRunes(r.after, max/3) + ")"
	}
	return cutRunes(r.text, max)
}
