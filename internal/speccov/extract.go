// Package speccov reads a task's text for the rules and worked examples it
// states, and cross-references them against the public tests in the
// workspace. The misses left on effbench are hidden-test rules the public
// tests do not reach; a model told "R3 and R5 have no public test" can
// spend its diligence where it is needed instead of everywhere.
//
// Everything here is deterministic. Nothing calls a model and nothing
// writes a file: Extract parses Markdown, Cover matches the literals and
// identifiers each rule names against the test files' text, and Harvest
// compiles the worked examples it can into self-contained shell probes
// that the agent package runs at the gate (internal/agent/speccov.go).
package speccov

import (
	"fmt"
	"regexp"
	"strings"
)

// Kind is what an item of the task text is.
type Kind int

const (
	// Rule is a stated requirement: a bullet, a table row, or a sentence
	// with must/never/exactly and the like in it.
	Rule Kind = iota
	// Example is a worked example inside a code block: `f(3) -> 9`, or a
	// shell command followed by its output.
	Example
)

func (k Kind) String() string {
	if k == Example {
		return "example"
	}
	return "rule"
}

// Item is one rule or example of the task text.
type Item struct {
	// ID is R1, R2, ... in the order the items appear.
	ID   string
	Kind Kind
	// Section is the heading the item sits under, "" before the first.
	Section string
	// Line is the item's first line in the text, 1-based.
	Line int
	// Text is the item folded onto one line.
	Text string
	// Anchors are the literals and identifiers the item names — what a
	// test that checks it would most likely contain too.
	Anchors []string
}

// Fence is one fenced code block of the task text, kept whole for Harvest:
// a CLI example is a command line and the lines after it.
type Fence struct {
	Section string
	Lang    string
	Line    int // the first line inside the fence, 1-based
	Lines   []string
}

// Spec is what Extract read from a task text.
type Spec struct {
	Items  []Item
	Fences []Fence
}

var (
	headingRe   = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	fenceRe     = regexp.MustCompile("^\\s*(```+|~~~+)\\s*([\\w+-]*)")
	bulletRe    = regexp.MustCompile(`^\s*(?:[*+-]|\d{1,3}[.)])\s+(.*)$`)
	tableSepRe  = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)
	modalRe     = regexp.MustCompile(`(?i)\b(must|never|should|shall|always|only|exactly|at (?:most|least)|required?|cannot|can't|may not|not allowed|raises?|rejects?|errors?|otherwise|unless|default)\b`)
	arrowRe     = regexp.MustCompile(`^(.*?\S)\s*(?:->|→|=>|==)\s*(\S.*)$`)
	promptRe    = regexp.MustCompile(`^\s*\$\s+(\S.*)$`)
	replRe      = regexp.MustCompile(`^\s*>>>\s+(\S.*)$`)
	backtickRe  = regexp.MustCompile("`([^`\n]+)`")
	dquoteRe    = regexp.MustCompile(`"((?:[^"\\\n]|\\.)*)"`)
	squoteRe    = regexp.MustCompile(`(?:^|[^\w'])'((?:[^'\\\n]|\\.)*)'`)
	numberRe    = regexp.MustCompile(`(?:^|[^\w.])(-?\d{2,}(?:\.\d+)?)\b`)
	identRe     = regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+\b|\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b|\b[a-z]+[A-Z][A-Za-z0-9]*\b|\b[A-Z][a-z0-9]+[A-Z][A-Za-z0-9]*\b|\b[A-Za-z_]\w*\(`)
	sentenceEnd = regexp.MustCompile(`[.!?]\s+`)
)

// minItemWords is the shortest bullet or row kept: "Exit code 0." states a
// rule, a one-word bullet in a list of names does not.
const minItemWords = 2

// Extract reads the rules and examples of a Markdown task text.
func Extract(md string) Spec {
	var sp Spec
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	section := ""
	add := func(kind Kind, line int, text string) {
		text = strings.Join(strings.Fields(text), " ")
		if len(strings.Fields(text)) < minItemWords && kind == Rule {
			return
		}
		sp.Items = append(sp.Items, Item{Kind: kind, Section: section, Line: line, Text: text})
	}
	var para []string
	paraLine := 0
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		text := strings.Join(para, " ")
		for _, s := range splitSentences(text) {
			if modalRe.MatchString(s) {
				add(Rule, paraLine, s)
			}
		}
		para = nil
	}
	var bullet []string
	bulletLine := 0
	flushBullet := func() {
		if len(bullet) > 0 {
			add(Rule, bulletLine, strings.Join(bullet, " "))
		}
		bullet = nil
	}
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		if m := fenceRe.FindStringSubmatch(ln); m != nil {
			flushPara()
			flushBullet()
			f := Fence{Section: section, Lang: strings.ToLower(m[2]), Line: i + 2}
			for i++; i < len(lines); i++ {
				if strings.HasPrefix(strings.TrimSpace(lines[i]), m[1][:3]) {
					break
				}
				f.Lines = append(f.Lines, lines[i])
			}
			sp.Fences = append(sp.Fences, f)
			for _, ex := range fenceExamples(f) {
				add(Example, ex.line, ex.text)
			}
			continue
		}
		trimmed := strings.TrimSpace(ln)
		switch {
		case trimmed == "":
			flushPara()
			flushBullet()
		case headingRe.MatchString(ln):
			flushPara()
			flushBullet()
			section = headingRe.FindStringSubmatch(ln)[1]
		case strings.HasPrefix(trimmed, "|"):
			flushPara()
			flushBullet()
			// A row followed by a separator row is the header.
			if tableSepRe.MatchString(trimmed) || i+1 < len(lines) && tableSepRe.MatchString(strings.TrimSpace(lines[i+1])) {
				continue
			}
			cells := strings.Split(strings.Trim(trimmed, "|"), "|")
			for j := range cells {
				cells[j] = strings.TrimSpace(cells[j])
			}
			add(Rule, i+1, strings.Join(cells, " | "))
		case bulletRe.MatchString(ln):
			flushPara()
			flushBullet()
			bullet, bulletLine = []string{bulletRe.FindStringSubmatch(ln)[1]}, i+1
		case len(bullet) > 0:
			// A continuation line of the bullet above.
			bullet = append(bullet, trimmed)
		default:
			if len(para) == 0 {
				paraLine = i + 1
			}
			para = append(para, trimmed)
		}
	}
	flushPara()
	flushBullet()
	for i := range sp.Items {
		sp.Items[i].ID = fmt.Sprintf("R%d", i+1)
		sp.Items[i].Anchors = anchors(sp.Items[i])
	}
	return sp
}

// splitSentences cuts a paragraph after each full stop, question or
// exclamation mark that a space follows.
func splitSentences(text string) []string {
	var out []string
	last := 0
	for _, loc := range sentenceEnd.FindAllStringIndex(text, -1) {
		out = append(out, strings.TrimSpace(text[last:loc[0]+1]))
		last = loc[1]
	}
	if rest := strings.TrimSpace(text[last:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

// fenceExample is one example line of a fence, before it is an Item.
type fenceExample struct {
	line int
	text string
}

// fenceExamples lists a fence's worked examples: each `lhs -> rhs` line,
// each REPL line with its output, and each shell command with the output
// lines that follow it up to the next command.
func fenceExamples(f Fence) []fenceExample {
	var out []fenceExample
	for j := 0; j < len(f.Lines); j++ {
		ln := strings.TrimSpace(f.Lines[j])
		switch {
		case promptRe.MatchString(ln):
			text := ln
			for k := j + 1; k < len(f.Lines) && !promptRe.MatchString(f.Lines[k]); k++ {
				if o := strings.TrimSpace(f.Lines[k]); o != "" {
					text += " ⏎ " + o
				}
			}
			out = append(out, fenceExample{f.Line + j, text})
		case replRe.MatchString(ln) && j+1 < len(f.Lines) && strings.TrimSpace(f.Lines[j+1]) != "" && !replRe.MatchString(f.Lines[j+1]):
			out = append(out, fenceExample{f.Line + j, replRe.FindStringSubmatch(ln)[1] + " -> " + strings.TrimSpace(f.Lines[j+1])})
			j++
		case arrowRe.MatchString(ln) && !strings.HasPrefix(ln, "//") && !strings.HasPrefix(ln, "#"):
			out = append(out, fenceExample{f.Line + j, ln})
		}
	}
	return out
}

// anchors are the literals and identifiers an item names, in the order
// they appear, without repeats.
func anchors(it Item) []string {
	var out []string
	seen := map[string]bool{}
	keep := func(s string) {
		s = strings.TrimSpace(s)
		if !anchorOK(s) || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	text := it.Text
	for _, m := range backtickRe.FindAllStringSubmatch(text, -1) {
		keep(m[1])
	}
	for _, m := range dquoteRe.FindAllStringSubmatch(text, -1) {
		keep(m[1])
	}
	for _, m := range squoteRe.FindAllStringSubmatch(text, -1) {
		keep(m[1])
	}
	if it.Kind == Example {
		// The two sides of an example are what a test of it would hold,
		// when they are short enough to be one literal.
		if m := arrowRe.FindStringSubmatch(text); m != nil && !strings.Contains(text, "⏎") {
			for _, side := range m[1:] {
				if !strings.ContainsAny(side, `"'`) && len(side) <= 40 {
					keep(side)
				}
			}
		}
	}
	for _, m := range numberRe.FindAllStringSubmatch(text, -1) {
		keep(m[1])
	}
	for _, m := range identRe.FindAllString(text, -1) {
		keep(strings.TrimSuffix(m, "("))
	}
	return out
}

// anchorStop are words that turn up in backticks and quotes but tell no
// rule from another.
var anchorStop = map[string]bool{
	"true": true, "false": true, "none": true, "null": true, "nil": true,
	"self": true, "str": true, "int": true, "len": true, "print": true,
	"error": true, "string": true, "list": true, "dict": true,
	"e.g": true, "i.e": true, "etc": true,
}

// anchorOK reports whether s is worth matching against tests: two or more
// characters with a letter or digit among them, and not a stop word.
func anchorOK(s string) bool {
	if len(s) < 2 || len(s) > 120 || anchorStop[strings.ToLower(s)] {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	}) >= 0
}
