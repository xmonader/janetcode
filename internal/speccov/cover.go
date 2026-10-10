package speccov

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Status is what the public tests say about one item.
type Status int

const (
	// Unanchored: the item names no literal or identifier specific
	// enough to look for, so the matrix says nothing about it.
	Unanchored Status = iota
	// Covered: a public test contains one of the item's specific anchors.
	Covered
	// Uncovered: the item has specific anchors and no public test
	// contains any of them. Hidden tests probably check it.
	Uncovered
)

func (s Status) String() string {
	switch s {
	case Covered:
		return "covered"
	case Uncovered:
		return "uncovered"
	}
	return "unanchored"
}

// Row is one item of the matrix.
type Row struct {
	Item
	Status Status
	// Specific are the item's anchors rare enough in the task text to
	// tell it from the other items; Hits are the ones a public test has.
	Specific, Hits []string
}

// Matrix is a task's items against its public tests.
type Matrix struct {
	Rows []Row
	// Tests are the public test files the matrix was built from.
	Tests []string
}

// Count returns how many rows have the status.
func (m Matrix) Count(s Status) int {
	n := 0
	for _, r := range m.Rows {
		if r.Status == s {
			n++
		}
	}
	return n
}

// Uncovered lists the rows no public test reaches.
func (m Matrix) Uncovered() []Row {
	var out []Row
	for _, r := range m.Rows {
		if r.Status == Uncovered {
			out = append(out, r)
		}
	}
	return out
}

// Specific returns the anchors of each item that are rare in the task
// text: an anchor shared by more than a few items (`minute`, the package
// name) is in every test whatever rule it checks, and would mark every
// item covered.
func Specific(items []Item) [][]string {
	df := map[string]int{}
	for _, it := range items {
		for _, a := range it.Anchors {
			df[a]++
		}
	}
	limit := max(2, len(items)/8)
	out := make([][]string, len(items))
	for i, it := range items {
		for _, a := range it.Anchors {
			if df[a] <= limit {
				out[i] = append(out[i], a)
			}
		}
	}
	return out
}

// Cover builds the matrix of the spec's items against the public tests,
// given as path → contents.
func Cover(sp Spec, tests map[string]string) Matrix {
	var m Matrix
	for p := range tests {
		m.Tests = append(m.Tests, p)
	}
	sort.Strings(m.Tests)
	var corpus strings.Builder
	for _, p := range m.Tests {
		corpus.WriteString(tests[p])
		corpus.WriteByte('\n')
	}
	text := corpus.String()
	spec := Specific(sp.Items)
	for i, it := range sp.Items {
		r := Row{Item: it, Specific: telling(spec[i])}
		for _, a := range r.Specific {
			if ContainsAnchor(text, a) {
				r.Hits = append(r.Hits, a)
			}
		}
		switch {
		case len(r.Specific) == 0:
			r.Status = Unanchored
		case len(r.Hits) > 0:
			r.Status = Covered
		default:
			r.Status = Uncovered
		}
		m.Rows = append(m.Rows, r)
	}
	return m
}

// ContainsAnchor reports whether text contains the anchor as a token of
// its own: an anchor that begins or ends with a word character must not
// run into one in text, so `60` is not found in `160` and `get` is not
// found in `target`.
func ContainsAnchor(text, a string) bool {
	if a == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(text[from:], a)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(a)
		okL := !isWord(a[0]) || i == 0 || !isWord(text[i-1])
		okR := !isWord(a[len(a)-1]) || end == len(text) || !isWord(text[end])
		if okL && okR {
			return true
		}
		from = i + 1
	}
}

func isWord(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// Touches lists the items whose specific anchors appear in text — the
// items a test body most likely exercises.
func Touches(m Matrix, text string) []Row {
	var out []Row
	for _, r := range m.Rows {
		for _, a := range r.Specific {
			if ContainsAnchor(text, a) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// Note limits: how many uncovered items the note lists, and how much of
// each item's text it quotes.
const (
	noteMaxItems = 12
	noteItemLen  = 160
)

// Note is what the model is told before its first edit, or "" when the
// matrix has nothing to say: no tests to compare against, or every
// anchored item covered.
func Note(m Matrix) string {
	unc := m.Uncovered()
	if len(m.Tests) == 0 || len(unc) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[harness] Spec coverage: the task text states %d rules and examples. %d of them name nothing that any public test (%s) contains, so the public tests cannot tell you whether you meet them; graded tests probably do. Check these yourself before you finish:\n",
		len(m.Rows), len(unc), strings.Join(shortList(m.Tests, 4), ", "))
	for i, r := range unc {
		if i == noteMaxItems {
			fmt.Fprintf(&b, "- and %d more\n", len(unc)-i)
			break
		}
		where := ""
		if r.Section != "" {
			where = " [" + r.Section + "]"
		}
		fmt.Fprintf(&b, "- %s%s %s\n", r.ID, where, clip(r.Text, noteItemLen))
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortList(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string(nil), xs[:n]...), fmt.Sprintf("%d more", len(xs)-n))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// bareIdentRe is an anchor that is only a name: `search`, `pkg.match`.
var bareIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// telling keeps the anchors that can tell a test of the item from any
// other test. A bare name is in every test that calls that function,
// whatever rule the test checks: "search returns the leftmost match"
// is not covered because a public test calls search. So when an item
// has literals besides names, only the literals count; an item that
// names nothing but functions (a signature) keeps its names.
func telling(anchors []string) []string {
	var lits []string
	for _, a := range anchors {
		if !bareIdentRe.MatchString(a) {
			lits = append(lits, a)
		}
	}
	if len(lits) > 0 {
		return lits
	}
	return anchors
}
