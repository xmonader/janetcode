// Package editpattern infers a rewrite rule from two structurally similar
// edits and finds the other sites it applies to: programming by example,
// in the style of PROSE, over tokens instead of a full AST.
//
// An edit is reduced to its core — the tokens left after the common
// prefix and suffix of old and new are stripped — plus a few tokens of
// context on either side. Two edits are similar when their cores and
// contexts line up token for token, where a position may differ only if
// both sides hold a word (identifier, number or string) of the same kind;
// such a position becomes a hole. So
//
//	foo(a)  ->  foo(a, ctx)
//	foo(b)  ->  foo(b, ctx)
//
// induce the rule  foo ( $0 )  ->  foo ( $0 , ctx ), which matches foo(c)
// and nothing that already has the second argument.
//
// This is the mechanism behind the "N more sites match" idea (wild idea 7).
// The offline pre-screen (Screen) found 3+ similar hunks in far fewer than
// the 10% of runs the idea needed, so nothing in the agent loop uses it;
// the package stays so the screen can be re-run on new campaigns.
package editpattern

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind classes a token.
type Kind int

const (
	Punct Kind = iota
	Ident
	Number
	String
)

// Token is one lexical token and its byte span in the source.
type Token struct {
	Kind       Kind
	Text       string
	Start, End int
}

// word reports whether a token may become a hole.
func (t Token) word() bool { return t.Kind != Punct }

// Tokenize splits src into identifiers, numbers, quoted strings and
// single punctuation runes, dropping whitespace. It is language-agnostic
// on purpose: Go, Python, TypeScript and Rust all lex close enough to this
// for the comparison it serves. A string with no closing quote runs to
// the end of the line.
func Tokenize(src string) []Token {
	var toks []Token
	for i := 0; i < len(src); {
		r, n := utf8.DecodeRuneInString(src[i:])
		switch {
		case unicode.IsSpace(r):
			i += n
		case r == '_' || unicode.IsLetter(r):
			j := i + n
			for j < len(src) {
				r2, n2 := utf8.DecodeRuneInString(src[j:])
				if r2 != '_' && !unicode.IsLetter(r2) && !unicode.IsDigit(r2) {
					break
				}
				j += n2
			}
			toks = append(toks, Token{Ident, src[i:j], i, j})
			i = j
		case unicode.IsDigit(r):
			j := i + n
			for j < len(src) && (isDigit(src[j]) || src[j] == '.' || src[j] == '_' || isLetter(src[j])) {
				j++
			}
			toks = append(toks, Token{Number, src[i:j], i, j})
			i = j
		case r == '"' || r == '\'' || r == '`':
			j := i + 1
			for j < len(src) && src[j] != byte(r) {
				if src[j] == '\\' && j+1 < len(src) {
					j++
				} else if src[j] == '\n' && r != '`' {
					break
				}
				j++
			}
			if j < len(src) && src[j] == byte(r) {
				j++
			}
			toks = append(toks, Token{String, src[i:j], i, j})
			i = j
		default:
			toks = append(toks, Token{Punct, src[i : i+n], i, i + n})
			i += n
		}
	}
	return toks
}

func isDigit(b byte) bool  { return b >= '0' && b <= '9' }
func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// Edit is one old -> new replacement the model made.
type Edit struct {
	Path     string
	Old, New string
}

// elem is one position of a rule: a literal token text, or a hole.
type elem struct {
	text string
	kind Kind
	hole int // -1 for a literal
}

func (e elem) String() string {
	if e.hole >= 0 {
		return "$" + string(rune('0'+e.hole%10))
	}
	return e.text
}

// Rule is an induced rewrite: Pattern is the context and the old core,
// matched against a file; the core is then replaced by the example's new
// core with its holes filled from the match.
type Rule struct {
	left, del, right []elem
	ins              []elem
	// gaps is the example's new core text around its tokens: gaps[i]
	// precedes ins[i], and gaps[len(ins)] follows the last one. It keeps
	// the example's spacing so a rewritten site reads like the edit did.
	gaps  []string
	holes int
}

// String renders the rule as "pattern -> replacement" for reports.
func (r Rule) String() string {
	join := func(parts ...[]elem) string {
		var s []string
		for _, p := range parts {
			for _, e := range p {
				s = append(s, e.String())
			}
		}
		return strings.Join(s, " ")
	}
	return join(r.left, r.del, r.right) + "  ->  " + join(r.left, r.ins, r.right)
}

// maxContext caps how many context tokens a rule anchors on each side.
// More context means fewer false sites but also fewer true ones once the
// call sites differ in what surrounds them.
const maxContext = 4

// maxCore keeps the rule to edits that are recognisably "the same small
// change": two rewrites of a whole function body line up token for token
// only by accident, and are not what a repeated-edit offer is for.
const maxCore = 40

// split is one edit's tokens cut into prefix, core and suffix.
type split struct {
	old, new       []Token
	newSrc         string
	pre            int // common prefix length
	oldSuf, newSuf int // index of the first suffix token in old and new
}

func cut(e Edit) split {
	o, n := Tokenize(e.Old), Tokenize(e.New)
	p := 0
	for p < len(o) && p < len(n) && o[p].Text == n[p].Text {
		p++
	}
	s := 0
	for s < len(o)-p && s < len(n)-p && o[len(o)-1-s].Text == n[len(n)-1-s].Text {
		s++
	}
	return split{old: o, new: n, newSrc: e.New, pre: p, oldSuf: len(o) - s, newSuf: len(n) - s}
}

// unifier assigns holes to differing word pairs, consistently: the same
// (a, b) pair is always the same hole, and a token on one side never maps
// to two different tokens on the other.
type unifier struct {
	pairs map[[2]string]int
	ab    map[string]string
	ba    map[string]string
}

func newUnifier() *unifier {
	return &unifier{pairs: map[[2]string]int{}, ab: map[string]string{}, ba: map[string]string{}}
}

// unify returns the elem for one aligned position, or ok false when the
// two tokens cannot be generalised (different punctuation, different
// kinds, or a mapping that contradicts an earlier one).
func (u *unifier) unify(a, b Token) (elem, bool) {
	if a.Text == b.Text {
		if prev, ok := u.ab[a.Text]; ok && prev != b.Text {
			return elem{}, false
		}
		if prev, ok := u.ba[b.Text]; ok && prev != a.Text {
			return elem{}, false
		}
		u.ab[a.Text], u.ba[b.Text] = b.Text, a.Text
		return elem{text: a.Text, kind: a.Kind, hole: -1}, true
	}
	if !a.word() || a.Kind != b.Kind {
		return elem{}, false
	}
	if prev, ok := u.ab[a.Text]; ok && prev != b.Text {
		return elem{}, false
	}
	if prev, ok := u.ba[b.Text]; ok && prev != a.Text {
		return elem{}, false
	}
	k := [2]string{a.Text, b.Text}
	h, ok := u.pairs[k]
	if !ok {
		h = len(u.pairs)
		u.pairs[k] = h
		u.ab[a.Text], u.ba[b.Text] = b.Text, a.Text
	}
	return elem{text: a.Text, kind: a.Kind, hole: h}, true
}

// Induce generalises two edits into one rule. It fails when the edits are
// the same site, when their cores differ in shape, when the new core uses
// a hole the pattern cannot bind, or when the pattern has nothing concrete
// to anchor on (a rule of holes and punctuation matches everywhere).
func Induce(a, b Edit) (Rule, bool) {
	if a.Path == b.Path && a.Old == b.Old {
		return Rule{}, false
	}
	sa, sb := cut(a), cut(b)
	delA, delB := sa.old[sa.pre:sa.oldSuf], sb.old[sb.pre:sb.oldSuf]
	insA, insB := sa.new[sa.pre:sa.newSuf], sb.new[sb.pre:sb.newSuf]
	if len(delA) != len(delB) || len(insA) != len(insB) || len(delA)+len(insA) == 0 {
		return Rule{}, false
	}
	if len(delA)+len(insA) > maxCore || !anyWord(delA, insA) {
		return Rule{}, false
	}
	u := newUnifier()
	var r Rule
	for i := range delA {
		e, ok := u.unify(delA[i], delB[i])
		if !ok {
			return Rule{}, false
		}
		r.del = append(r.del, e)
	}
	// Context grows outward from the core and stops at the first position
	// the two edits disagree on in shape.
	for i := 1; i <= maxContext && sa.pre-i >= 0 && sb.pre-i >= 0; i++ {
		e, ok := u.unify(sa.old[sa.pre-i], sb.old[sb.pre-i])
		if !ok {
			break
		}
		r.left = append([]elem{e}, r.left...)
	}
	for i := 0; i < maxContext && sa.oldSuf+i < len(sa.old) && sb.oldSuf+i < len(sb.old); i++ {
		e, ok := u.unify(sa.old[sa.oldSuf+i], sb.old[sb.oldSuf+i])
		if !ok {
			break
		}
		r.right = append(r.right, e)
	}
	for i := range insA {
		e, ok := u.unify(insA[i], insB[i])
		if !ok {
			return Rule{}, false
		}
		r.ins = append(r.ins, e)
	}
	// A hole at the outer edge of the context only narrows the match ("a
	// word must precede this") without anchoring it; drop it unless the
	// rule uses the word elsewhere.
	for len(r.left) > 0 && r.left[0].hole >= 0 && uses(r.left[0].hole, r.left[1:], r.del, r.right, r.ins) == 0 {
		r.left = r.left[1:]
	}
	for n := len(r.right); n > 0 && r.right[n-1].hole >= 0 && uses(r.right[n-1].hole, r.left, r.del, r.right[:n-1], r.ins) == 0; n-- {
		r.right = r.right[:n-1]
	}
	for _, e := range r.ins {
		if e.hole >= 0 && uses(e.hole, r.left, r.del, r.right) == 0 {
			return Rule{}, false
		}
	}
	if len(r.del) == 0 && len(r.left)+len(r.right) == 0 {
		return Rule{}, false
	}
	if !anchored(r.left, r.del, r.right) {
		return Rule{}, false
	}
	r.holes = len(u.pairs)
	r.gaps = gaps(sa)
	return r, true
}

// uses counts the positions holding hole h.
func uses(h int, parts ...[]elem) int {
	n := 0
	for _, p := range parts {
		for _, e := range p {
			if e.hole == h {
				n++
			}
		}
	}
	return n
}

func anyWord(parts ...[]Token) bool {
	for _, p := range parts {
		for _, t := range p {
			if t.word() {
				return true
			}
		}
	}
	return false
}

// anchored requires a literal word in the pattern, and at least two
// pattern tokens in all.
func anchored(parts ...[]elem) bool {
	n, lit := 0, false
	for _, p := range parts {
		for _, e := range p {
			n++
			if e.hole < 0 && e.kind != Punct {
				lit = true
			}
		}
	}
	return lit && n >= 2
}

// gaps records the text between the example's new core tokens, from the
// end of the prefix to the start of the suffix.
func gaps(s split) []string {
	start := 0
	if s.pre > 0 {
		start = s.new[s.pre-1].End
	}
	end := len(s.newSrc)
	if s.newSuf < len(s.new) {
		end = s.new[s.newSuf].Start
	}
	out := []string{}
	at := start
	for _, t := range s.new[s.pre:s.newSuf] {
		out = append(out, s.newSrc[at:t.Start])
		at = t.End
	}
	return append(out, s.newSrc[at:end])
}

// Site is one place in a file the rule matches: Start and End are the
// byte span to replace and Text what replaces it.
type Site struct {
	Start, End int
	Line       int
	Text       string
}

// Find returns every non-overlapping site in src the rule matches, in
// order. A site the rule would leave unchanged is skipped.
func (r Rule) Find(src string) []Site {
	toks := Tokenize(src)
	pat := append(append(append([]elem{}, r.left...), r.del...), r.right...)
	var sites []Site
	for i := 0; i+len(pat) <= len(toks); {
		bind, ok := match(pat, toks[i:i+len(pat)])
		if !ok {
			i++
			continue
		}
		s := r.site(src, toks[i:i+len(pat)], bind)
		if src[s.Start:s.End] == s.Text {
			i++
			continue
		}
		s.Line = 1 + strings.Count(src[:s.Start], "\n")
		sites = append(sites, s)
		i += len(pat)
	}
	return sites
}

func match(pat []elem, toks []Token) (map[int]string, bool) {
	bind := map[int]string{}
	used := map[string]int{}
	for i, e := range pat {
		t := toks[i]
		if e.hole < 0 {
			if t.Text != e.text {
				return nil, false
			}
			continue
		}
		if t.Kind != e.kind {
			return nil, false
		}
		if v, ok := bind[e.hole]; ok && v != t.Text {
			return nil, false
		}
		if h, ok := used[t.Text]; ok && h != e.hole {
			return nil, false
		}
		bind[e.hole], used[t.Text] = t.Text, e.hole
	}
	return bind, true
}

// site builds the replacement for one match: the span runs from the end
// of the left context to the start of the right context, as the example's
// core did, so the context itself is never rewritten.
func (r Rule) site(src string, toks []Token, bind map[int]string) Site {
	nl, nd := len(r.left), len(r.del)
	var start, end int
	switch {
	case nl > 0:
		start = toks[nl-1].End
	default:
		start = toks[0].Start
	}
	switch {
	case len(r.right) > 0:
		end = toks[nl+nd].Start
	case nd > 0:
		end = toks[nl+nd-1].End
	default:
		end = start
	}
	var b strings.Builder
	for i, e := range r.ins {
		b.WriteString(r.gaps[i])
		if e.hole >= 0 {
			b.WriteString(bind[e.hole])
		} else {
			b.WriteString(e.text)
		}
	}
	b.WriteString(r.gaps[len(r.ins)])
	text := b.String()
	// With no context on a side, the span starts or ends at a token, so
	// the example's whitespace on that side belongs to the file, not to
	// the replacement.
	if nl == 0 {
		text = strings.TrimLeft(text, " \t\n")
	}
	if len(r.right) == 0 && nd > 0 {
		text = strings.TrimRight(text, " \t\n")
	}
	return Site{Start: start, End: end, Text: text}
}

// Apply rewrites src at the given sites, which must be in order and not
// overlap (as Find returns them).
func Apply(src string, sites []Site) string {
	var b strings.Builder
	at := 0
	for _, s := range sites {
		b.WriteString(src[at:s.Start])
		b.WriteString(s.Text)
		at = s.End
	}
	b.WriteString(src[at:])
	return b.String()
}

// Predicts reports whether the rule, applied to e.Old, gives e.New up to
// whitespace: whether a harness offering the rule would have written
// exactly the edit the model went on to write by hand.
func (r Rule) Predicts(e Edit) bool {
	sites := r.Find(e.Old)
	if len(sites) == 0 {
		return false
	}
	return sameTokens(Apply(e.Old, sites), e.New)
}

func sameTokens(a, b string) bool {
	ta, tb := Tokenize(a), Tokenize(b)
	if len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i].Text != tb[i].Text {
			return false
		}
	}
	return true
}
