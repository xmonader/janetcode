// Package preresolve looks up what a request names before the model is
// asked anything: the files and identifiers it mentions are resolved
// against the repository map's index, and their definitions, plus the
// tests that exercise them, go with the first message under a byte
// budget.
//
// It is the large-repository half of inlining. A repository small enough
// is sent whole (tool.SmallRepo), which took 41% of the model calls out of
// a lean-execution A/B; a repository too big for that is explored one call
// at a time, and on the 58-file explore fixture 40–54% of a run went by
// before the first edit. Most of those calls find a definition the request
// already named. Doing that lookup in the harness costs no model call, and
// the model starts from the code instead of from a search for it.
//
// What counts as named, strongest first:
//
//   - a path: a token that is an indexed file, or the tail of one;
//   - an identifier: a backticked name, or a code-shaped one in the prose
//     (CamelCase, snake_case, Type.Method), defined somewhere in the index;
//   - a word: a plain word of the request that is a part of exactly one
//     definition's compound name in the whole index ("refund" for
//     RefundWindowDays). Weakest, so it fills what budget is left.
//
// A name defined in more than three files is too generic to mean anything
// in particular (New, Run, String) and is left to the model. Nothing here
// runs a model, a language server or a command; it reads files under the
// index's root and nothing else.
package preresolve

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/repomap"
)

// DefaultBudget is the most the resolved block may add to the first
// message, in bytes: about the size of the two or three files a model
// reads before its first edit on a mid-sized repository, and twice the
// per-turn repository map.
const DefaultBudget = 12 << 10

// Limits on what one request may pull in.
const (
	// wholeFileMax: a named file up to this size is sent whole, and
	// recorded as read; a bigger one gets its outline.
	wholeFileMax = 6 << 10
	// maxDefLines bounds one definition's excerpt.
	maxDefLines = 60
	// maxDefiners: a name defined in more files is too generic to resolve.
	maxDefiners = 3
	// maxTestsPerSymbol bounds the covering tests shown per definition.
	maxTestsPerSymbol = 2
	// maxWordSymbols bounds the word tier.
	maxWordSymbols = 3
	maxMentions    = 16
)

// Kinds of mention, strongest first.
const (
	KindPath  = "path"
	KindIdent = "identifier"
	KindWord  = "word"
)

// Mention is one thing the request names.
type Mention struct {
	Text string
	Kind string
}

// Result is what a request resolved to.
type Result struct {
	// Text is the block for the first message, "" when nothing resolved.
	Text string
	// Whole are the files sent whole (relative to the index root), with
	// their contents, so the caller can record them as read.
	Whole map[string][]byte
	// Symbols are the definitions shown, as path:line name.
	Symbols []string
	// Tests are the covering tests shown, as path:line.
	Tests []string
	// Dropped counts items that resolved but did not fit the budget.
	Dropped int
}

var (
	backtickRe = regexp.MustCompile("`([^`\n]{1,160})`")
	tokenRe    = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*`)
	pathRe     = regexp.MustCompile(`[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)*\.[A-Za-z0-9]{1,6}|[A-Za-z0-9_.\-]+(?:/[A-Za-z0-9_.\-]+)+`)
	wordRe     = regexp.MustCompile(`[a-z]{4,}`)
)

// stopwords are words of a request that name no code, however many
// symbols happen to contain them.
var stopwords = map[string]bool{"this": true, "that": true, "with": true, "from": true, "into": true, "make": true, "file": true, "files": true, "function": true, "code": true, "test": true, "tests": true, "please": true, "should": true, "when": true, "then": true, "what": true, "where": true, "which": true, "have": true, "does": true, "also": true, "each": true, "only": true, "same": true, "change": true, "update": true, "write": true, "read": true, "find": true, "both": true, "other": true, "instead": true, "keep": true, "keeping": true, "exactly": true, "single": true, "module": true, "package": true, "packages": true, "repository": true, "many": true, "number": true, "value": true, "values": true, "defines": true, "define": true, "building": true, "build": true, "name": true, "names": true, "type": true, "types": true, "method": true, "methods": true, "error": true, "errors": true, "return": true, "returns": true, "data": true, "list": true, "string": true, "true": true, "false": true, "there": true, "their": true, "they": true, "them": true, "must": true, "never": true, "always": true, "used": true, "uses": true, "using": true, "work": true, "works": true, "like": true, "than": true, "more": true, "less": true, "long": true, "after": true, "before": true, "first": true, "last": true, "once": true, "need": true, "needs": true, "implement": true, "task": true, "pass": true, "fail": true, "fails": true, "check": true, "checks": true, "constant": true, "variable": true}

// Mentions returns what text names, in the order it names them, each
// once, without looking anything up: the candidates Resolve checks
// against the index. Words are not included; which words count depends
// on the index.
func Mentions(text string) []Mention {
	var out []Mention
	seen := map[string]bool{}
	add := func(s, kind string) {
		s = strings.Trim(s, ".,;:()[]{}\"'")
		if s == "" || seen[s] || len(out) >= maxMentions {
			return
		}
		seen[s] = true
		out = append(out, Mention{Text: s, Kind: kind})
	}
	ticked := map[string]bool{}
	for _, m := range backtickRe.FindAllStringSubmatch(text, -1) {
		inner := strings.TrimSpace(m[1])
		// `Foo()`, `pkg.Foo(x)`, `path/to/x.go:12`: the name, not the call
		// or the position.
		if i := strings.IndexAny(inner, "( "); i > 0 {
			inner = inner[:i]
		}
		if i := strings.IndexByte(inner, ':'); i > 0 {
			inner = inner[:i]
		}
		if strings.Contains(inner, "/") || looksLikeFile(inner) {
			add(inner, KindPath)
		} else if tokenRe.MatchString(inner) && tokenRe.FindString(inner) == inner {
			ticked[inner] = true
			add(inner, KindIdent)
		}
	}
	for _, p := range pathRe.FindAllString(text, -1) {
		if strings.Contains(p, "/") || looksLikeFile(p) {
			if i := strings.IndexByte(p, ':'); i > 0 {
				p = p[:i]
			}
			add(p, KindPath)
		}
	}
	for _, t := range tokenRe.FindAllString(text, -1) {
		if ticked[t] || !codeShaped(t) || looksLikeFile(t) {
			continue
		}
		add(t, KindIdent)
	}
	return out
}

// looksLikeFile is a token ending in a source or config extension.
func looksLikeFile(s string) bool {
	ext := strings.ToLower(filepath.Ext(s))
	switch ext {
	case ".go", ".py", ".pyi", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".rs", ".java", ".kt", ".rb", ".c", ".h", ".cc", ".cpp", ".hpp", ".cs", ".swift", ".zig", ".php", ".scala", ".ex", ".exs", ".json", ".yaml", ".yml", ".toml", ".md", ".sql", ".proto", ".sh":
		return len(s) > len(ext)
	}
	return false
}

// codeShaped is an identifier no English word looks like: an inner
// capital (parseConfig, HTTPServer), an underscore (max_retries), or a
// selector (Cache.Get). A capitalised first word of a sentence is not.
func codeShaped(t string) bool {
	if len(t) < 3 {
		return false
	}
	if strings.Contains(t, ".") {
		parts := strings.Split(t, ".")
		// "e.g", "i.e" and version-ish dots are not selectors.
		return len(parts[0]) >= 2 && len(parts[len(parts)-1]) >= 2 && (codeShaped(parts[len(parts)-1]) || isUpper(parts[len(parts)-1][0]) || isUpper(parts[0][0]))
	}
	if strings.Contains(strings.Trim(t, "_"), "_") {
		return true
	}
	for i := 1; i < len(t); i++ {
		if isUpper(t[i]) && !isUpper(t[i-1]) {
			return true
		}
	}
	// HTTP, JSON: all capitals is an acronym, not a name.
	return false
}

func isUpper(c byte) bool { return c >= 'A' && c <= 'Z' }

// Resolve looks up what task names in ix and renders the definitions and
// covering tests it finds within budget bytes (DefaultBudget when 0).
// Files are read from ix.Root, as the index was built.
func Resolve(ix *repomap.Index, task string, budget int) Result {
	res := Result{Whole: map[string][]byte{}}
	if ix == nil || len(ix.Files) == 0 || strings.TrimSpace(task) == "" {
		return res
	}
	if budget <= 0 {
		budget = DefaultBudget
	}
	r := &resolver{ix: ix, files: map[string][]string{}, shown: map[string]bool{}}
	var items []item
	symbols := []site{}
	for _, m := range Mentions(task) {
		switch m.Kind {
		case KindPath:
			if p, ok := r.path(m.Text); ok && !r.shown["file:"+p] {
				r.shown["file:"+p] = true
				if it, ok := r.fileItem(p); ok {
					items = append(items, it)
				}
			}
		case KindIdent:
			symbols = append(symbols, r.definitions(m.Text)...)
		}
	}
	symbols = append(symbols, r.wordDefinitions(task)...)
	var tests []item
	for _, s := range symbols {
		key := fmt.Sprintf("def:%s:%d", s.path, s.line)
		if r.shown[key] || r.shown["file:"+s.path] {
			continue
		}
		r.shown[key] = true
		if it, ok := r.defItem(s); ok {
			items = append(items, it)
		}
		tests = append(tests, r.testItems(s)...)
	}
	// Tests after every definition: a definition is what the model came
	// for, a test is what it checks the change against.
	items = append(items, tests...)

	var b strings.Builder
	const head = "<resolved>\nWhat the request names, looked up in the repository before your first call. Excerpts are the current file contents at the lines shown; read the file for anything outside them.\n"
	const foot = "</resolved>"
	used := len(head) + len(foot)
	for _, it := range items {
		if used+len(it.text) > budget {
			res.Dropped++
			continue
		}
		used += len(it.text)
		b.WriteString(it.text)
		switch {
		case it.whole != nil:
			res.Whole[it.path] = it.whole
		case it.test:
			res.Tests = append(res.Tests, it.label)
		default:
			res.Symbols = append(res.Symbols, it.label)
		}
	}
	if b.Len() == 0 {
		return res
	}
	res.Text = head + b.String() + foot
	return res
}

// site is one definition to show.
type site struct {
	path, name, kind string
	line             int
}

// item is one rendered piece of the block.
type item struct {
	text  string
	path  string
	label string
	whole []byte
	test  bool
}

type resolver struct {
	ix    *repomap.Index
	files map[string][]string // path → lines, read once
	shown map[string]bool
}

// path resolves a mentioned path to an indexed file: the file itself, or
// the one file that ends in it. Two files ending in the same tail are
// ambiguous and resolve to neither.
func (r *resolver) path(p string) (string, bool) {
	p = strings.TrimPrefix(filepath.ToSlash(p), "./")
	// The whole list is scanned before giving up on a shared tail: the
	// file the path names exactly may come after two that end in it.
	var hit string
	ambiguous := false
	for _, f := range r.ix.Files {
		if f.Path == p {
			return p, true
		}
		if strings.HasSuffix(f.Path, "/"+p) {
			ambiguous = ambiguous || hit != ""
			hit = f.Path
		}
	}
	if ambiguous {
		return "", false
	}
	return hit, hit != ""
}

// definitions finds where name is defined, when few enough files do.
func (r *resolver) definitions(name string) []site {
	rad, ok := r.ix.BlastRadius(name, false)
	if !ok || len(rad.Definers) == 0 || len(rad.Definers) > maxDefiners {
		return nil
	}
	var out []site
	for _, s := range rad.Sites {
		if s.Line > 0 {
			out = append(out, site{path: s.Path, name: s.Name, kind: s.Kind, line: s.Line})
		}
	}
	return out
}

// wordDefinitions is the word tier: a plain word of the request that is
// one part of exactly one definition's compound name in the whole index
// (refund in RefundWindowDays). A word that is a part of two definitions
// is a theme of the codebase, not a pointer into it, and a definition
// whose whole name is one word (Survey, alpha) is as likely to be
// English as code: on recorded prompts with no code in them ("reply with
// hello world") the looser rule pulled in three unrelated definitions.
func (r *resolver) wordDefinitions(task string) []site {
	// Only the prose: a backticked or code-shaped name was resolved as
	// an identifier already, and its parts (Cache in Cache.Get) are not
	// what the request is about.
	prose := backtickRe.ReplaceAllString(task, " ")
	prose = tokenRe.ReplaceAllStringFunc(prose, func(t string) string {
		if codeShaped(t) {
			return " "
		}
		return t
	})
	words := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(prose), -1) {
		if len(w) >= 5 && !stopwords[w] {
			words[w] = true
		}
	}
	if len(words) == 0 {
		return nil
	}
	byWord := map[string][]site{}
	for _, f := range r.ix.Files {
		for _, d := range f.Defs {
			parts := splitIdent(d.Name)
			for _, part := range parts {
				for _, w := range []string{part, part + "s", part + "es"} {
					if words[w] {
						s := site{f.Path, d.Name, d.Kind, d.Line}
						if len(parts) < 2 || isTestPath(f.Path) {
							// Counted, so it makes the word ambiguous, but
							// never shown.
							s.line = 0
						}
						byWord[w] = append(byWord[w], s)
					}
				}
			}
		}
	}
	keys := make([]string, 0, len(byWord))
	for w := range byWord {
		keys = append(keys, w)
	}
	sort.Strings(keys)
	var out []site
	for _, w := range keys {
		if hs := byWord[w]; len(hs) == 1 && hs[0].line > 0 && len(out) < maxWordSymbols {
			out = append(out, hs[0])
		}
	}
	return out
}

// lines reads one indexed file, split into lines, once.
func (r *resolver) lines(p string) ([]string, []byte, bool) {
	if ls, ok := r.files[p]; ok {
		return ls, nil, ls != nil
	}
	data, err := os.ReadFile(filepath.Join(r.ix.Root, filepath.FromSlash(p)))
	if err != nil {
		r.files[p] = nil
		return nil, nil, false
	}
	ls := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	r.files[p] = ls
	return ls, data, true
}

// fileItem is a named file: whole when it is small, else its outline.
func (r *resolver) fileItem(p string) (item, bool) {
	abs := filepath.Join(r.ix.Root, filepath.FromSlash(p))
	st, err := os.Lstat(abs)
	if err != nil || !st.Mode().IsRegular() {
		return item{}, false
	}
	if st.Size() <= wholeFileMax {
		data, err := os.ReadFile(abs)
		if err != nil {
			return item{}, false
		}
		text := fmt.Sprintf("<file path=%q whole=\"true\">\n%s", p, data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			text += "\n"
		}
		return item{text: text + "</file>\n", path: p, label: p, whole: data}, true
	}
	var defs []string
	for _, f := range r.ix.Files {
		if f.Path != p {
			continue
		}
		for _, d := range f.Defs {
			defs = append(defs, fmt.Sprintf("  %d %s %s", d.Line, d.Kind, d.Name))
		}
	}
	if len(defs) == 0 {
		return item{}, false
	}
	if len(defs) > 40 {
		defs = append(defs[:40], fmt.Sprintf("  … %d more", len(defs)-40))
	}
	return item{text: fmt.Sprintf("<outline path=%q bytes=\"%d\">\n%s\n</outline>\n", p, st.Size(), strings.Join(defs, "\n")), path: p, label: p + " (outline)"}, true
}

// defItem is one definition's excerpt.
func (r *resolver) defItem(s site) (item, bool) {
	ls, _, ok := r.lines(s.path)
	if !ok || s.line < 1 || s.line > len(ls) {
		return item{}, false
	}
	from, to := Extent(s.path, ls, s.line-1)
	return item{
		text:  excerpt("definition", fmt.Sprintf("symbol=%q path=%q", s.name, s.path), ls, from, to),
		path:  s.path,
		label: fmt.Sprintf("%s:%d %s", s.path, s.line, s.name),
	}, true
}

// testItems are the test functions that mention s by name, in the test
// files the index says use it.
func (r *resolver) testItems(s site) []item {
	bare := s.name
	if i := strings.LastIndexByte(bare, '.'); i >= 0 {
		bare = bare[i+1:]
	}
	rad, ok := r.ix.BlastRadius(s.name, false)
	if !ok {
		return nil
	}
	var cands []string
	for _, u := range rad.Referencers {
		if isTestPath(u.Path) {
			cands = append(cands, u.Path)
		}
	}
	// A test beside the definition, in the definer itself (Python's
	// test_ functions, Rust's mod tests) is a covering test too.
	for _, d := range rad.Definers {
		if isTestPath(d) {
			cands = append(cands, d)
		}
	}
	var out []item
	for _, p := range cands {
		ls, _, ok := r.lines(p)
		if !ok {
			continue
		}
		for i, l := range ls {
			if len(out) >= maxTestsPerSymbol {
				return out
			}
			if !containsWord(l, bare) {
				continue
			}
			start := enclosingTest(ls, i)
			if start < 0 {
				continue
			}
			key := fmt.Sprintf("test:%s:%d", p, start)
			if r.shown[key] {
				continue
			}
			r.shown[key] = true
			from, to := Extent(p, ls, start)
			out = append(out, item{
				text:  excerpt("test", fmt.Sprintf("covers=%q path=%q", s.name, p), ls, from, to),
				path:  p,
				label: fmt.Sprintf("%s:%d", p, from+1),
				test:  true,
			})
		}
	}
	return out
}

// excerpt renders lines[from..to] with the range in the tag.
func excerpt(tag, attrs string, ls []string, from, to int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<%s %s lines=\"%d-%d\">\n", tag, attrs, from+1, to+1)
	for _, l := range ls[from : to+1] {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if more := to - from + 1; more >= maxDefLines {
		fmt.Fprintf(&b, "… (cut at %d lines)\n", maxDefLines)
	}
	fmt.Fprintf(&b, "</%s>\n", tag)
	return b.String()
}

var testFuncRe = regexp.MustCompile(`^\s*(func (Test|Benchmark|Fuzz|Example)\w*\(|(async\s+)?def test\w*\(|(it|test|describe)\s*\(|#\[(tokio::)?test\]|@Test\b|fn test_\w*\()`)

// enclosingTest is the line of the test function that holds line i, or
// -1: the nearest test opener above it that has not already closed.
func enclosingTest(ls []string, i int) int {
	for j := i; j >= 0 && i-j < 200; j-- {
		if testFuncRe.MatchString(ls[j]) {
			_, to := Extent("", ls, j)
			if to >= i {
				return j
			}
			return -1
		}
	}
	return -1
}

func isTestPath(p string) bool {
	base := filepath.Base(p)
	return strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") || strings.HasSuffix(strings.TrimSuffix(base, filepath.Ext(base)), "_test") || strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.Contains("/"+p, "/tests/") || strings.Contains("/"+p, "/test/")
}

func splitIdent(s string) []string {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		s = s[i+1:]
	}
	var parts []string
	start := 0
	for i := 1; i <= len(s); i++ {
		if i == len(s) || s[i] == '_' || (isUpper(s[i]) && s[i-1] >= 'a' && s[i-1] <= 'z') {
			if p := strings.ToLower(strings.Trim(s[start:i], "_")); p != "" {
				parts = append(parts, p)
			}
			start = i
		}
	}
	return parts
}

func containsWord(text, word string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], word)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !repomap.IsIdentByte(text[j-1])
		after := j+len(word) == len(text) || !repomap.IsIdentByte(text[j+len(word)])
		if before && after {
			return true
		}
		i = j + 1
	}
}
