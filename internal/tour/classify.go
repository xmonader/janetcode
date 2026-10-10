// Package tour orders what a turn changed for a person to review: riskiest
// hunk first, each with the intent the model stated for it, and the
// mechanical churn — reformatting, consistent renames — collapsed behind a
// count instead of read line by line. It also keeps the comprehension-debt
// arithmetic: how many changed lines were approved without anyone having
// had them in front of them, per package.
//
// Everything here is deterministic and reads only the provenance record
// (internal/change). Nothing calls a model, nothing writes a file; how far
// a changed file reaches comes from a caller-supplied function (the
// composition root wires internal/impact to it), so the package has no
// view of the repository of its own.
package tour

import (
	"go/scanner"
	"go/token"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/project"
)

// Class is what kind of change a hunk is, in the order a reviewer should
// care about it: Behaviour first, the mechanical classes last.
type Class int

const (
	// Behaviour is a change to what the program does: anything the
	// checks below could not prove is something less.
	Behaviour Class = iota
	// Test is a change to a test file that is not mechanical. It ranks
	// under Behaviour but above the rest: a test edit is how a green run
	// is made without the code being right.
	Test
	// Comment changes comments only, or a prose file (README, notes).
	Comment
	// Format changes layout only: the token stream is identical on both
	// sides, so the change is invisible to a compiler (indentation counts
	// as a token in the languages where it means something).
	Format
	// Rename changes identifiers only, consistently and to names the file
	// did not use before, with every other token the same.
	Rename
)

func (c Class) String() string {
	switch c {
	case Test:
		return "test"
	case Comment:
		return "comment"
	case Format:
		return "format"
	case Rename:
		return "rename"
	}
	return "behaviour"
}

// Mechanical says the class is churn a reviewer can take on trust: the
// tour collapses it behind a count, and it is not comprehension debt.
func (c Class) Mechanical() bool { return c == Format || c == Rename }

// Classify decides one hunk's class. removed and added are the hunk's
// '-' and '+' lines without their prefix. scope is the names in use on
// each side of the change set the hunk belongs to, which the rename check
// needs: renaming x to a y that already existed somewhere is a behaviour
// change that is token-for-token a rename.
//
// The check is token equality, not AST equality: a hunk is a fragment
// that rarely parses on its own, and a tokenizer reads fragments fine.
// For Go the standard library's scanner supplies the tokens; for other
// languages a small lexer that knows identifiers, strings and the two
// common comment syntaxes does. Both err towards Behaviour: a hunk the
// lexer cannot account for exactly is never called mechanical.
func Classify(path string, removed, added []string, scope *Scope) Class {
	lang := langOf(path)
	if lang.prose {
		if squash(removed) == squash(added) {
			return Format
		}
		return Comment
	}
	old, new := lang.tokens(strings.Join(removed, "")), lang.tokens(strings.Join(added, ""))
	switch {
	case sameTokens(old, new, true):
		return Format
	case sameTokens(old, new, false):
		return Comment
	case !importLine(removed) && !importLine(added) && renamed(lang, old, new, scope):
		return Rename
	case project.IsTestFile(path):
		return Test
	}
	return Behaviour
}

// Scope is the identifiers in use before and after a set of changes,
// across every file in it. A rename only counts as one when the new name
// is in none of the files before and the old name in none of them after.
type Scope struct {
	was, now map[string]bool
}

// NewScope is an empty scope.
func NewScope() *Scope { return &Scope{was: map[string]bool{}, now: map[string]bool{}} }

// Add takes in one file's content on both sides of the change set.
func (s *Scope) Add(path, before, after string) {
	lang := langOf(path)
	if lang.prose {
		return
	}
	for n := range idents(lang.tokens(before)) {
		s.was[n] = true
	}
	for n := range idents(lang.tokens(after)) {
		s.now[n] = true
	}
}

// importLine reports whether any line imports a name from elsewhere.
// Swapping an imported name for another is checkable only against the
// module it comes from, which is usually not in the change set: "from
// .tz import UTC" to "from .tz import localize" passes every check
// renamed makes and imports a different function.
func importLine(lines []string) bool {
	for _, l := range lines {
		t := strings.TrimSpace(l)
		for _, p := range []string{"import ", "from ", "use ", "#include", "require", "extern crate", "using "} {
			if strings.HasPrefix(t, p) {
				return true
			}
		}
		if strings.Contains(t, "require(") || strings.Contains(t, "import(") {
			return true
		}
	}
	return false
}

// squash is the text with every whitespace character dropped.
func squash(lines []string) string {
	return strings.Join(strings.Fields(strings.Join(lines, "")), "")
}

// tok is one lexical token. kind is 'i' for an identifier, 'c' for a
// comment, 'n' for a line's indentation in an indentation-sensitive
// language, and 'o' for everything else (operators, literals, keywords).
type tok struct {
	kind byte
	text string
}

// sameTokens compares two token streams, with comments or without.
func sameTokens(a, b []tok, withComments bool) bool {
	if !withComments {
		a, b = code(a), code(b)
	}
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// code is the stream without its comments.
func code(ts []tok) []tok {
	out := make([]tok, 0, len(ts))
	for _, t := range ts {
		if t.kind != 'c' {
			out = append(out, t)
		}
	}
	return out
}

// renamed reports whether new is old with identifiers substituted one to
// one, consistently, and only to names the scope never used before and
// from names it no longer uses after. The last two are what keep "return a" → "return
// b" (both live variables) out: that is the most dangerous one-token
// change there is, and token-for-token it looks exactly like a rename.
// Comments may differ (a rename usually rewrites the ones that mention
// the old name). A hunk that only adds or only removes is never a rename.
func renamed(lang language, old, new []tok, scope *Scope) bool {
	old, new = code(old), code(new)
	if len(old) == 0 || len(old) != len(new) {
		return false
	}
	fwd, back := map[string]string{}, map[string]string{}
	for i := range old {
		a, b := old[i], new[i]
		if a == b {
			continue
		}
		if a.kind != 'i' || b.kind != 'i' || lang.keyword(a.text) || lang.keyword(b.text) {
			return false
		}
		if to, ok := fwd[a.text]; ok && to != b.text {
			return false
		}
		if from, ok := back[b.text]; ok && from != a.text {
			return false
		}
		fwd[a.text], back[b.text] = b.text, a.text
	}
	if len(fwd) == 0 {
		return false
	}
	for from, to := range fwd {
		if scope == nil || scope.was[to] || scope.now[from] {
			return false
		}
	}
	return true
}

// idents is the set of identifiers in a token stream.
func idents(ts []tok) map[string]bool {
	out := map[string]bool{}
	for _, t := range ts {
		if t.kind == 'i' {
			out[t.text] = true
		}
	}
	return out
}

// language is what the lexer needs to know about a file's language.
type language struct {
	goSrc  bool // tokenized by go/scanner
	prose  bool // not code: any non-whitespace change is a Comment
	hash   bool // # starts a line comment
	slash  bool // // and /* */ are comments
	indent bool // leading whitespace is syntax
}

var (
	hashExt   = set(".py", ".pyi", ".sh", ".bash", ".zsh", ".rb", ".pl", ".r", ".toml", ".yaml", ".yml", ".mk", ".cfg", ".ini", ".nix", ".tf")
	slashExt  = set(".c", ".h", ".cc", ".cpp", ".hpp", ".cxx", ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".java", ".kt", ".kts", ".rs", ".swift", ".cs", ".scala", ".php", ".dart", ".zig", ".proto", ".css", ".scss")
	indentExt = set(".py", ".pyi", ".yaml", ".yml", ".mk")
	proseExt  = set(".md", ".markdown", ".rst", ".txt", ".adoc")
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func langOf(path string) language {
	base := filepath.Base(path)
	ext := strings.ToLower(filepath.Ext(base))
	if base == "Makefile" || base == "makefile" || base == "GNUmakefile" {
		return language{hash: true, indent: true}
	}
	if ext == ".go" {
		return language{goSrc: true}
	}
	if proseExt[ext] && !strings.HasPrefix(base, ".") {
		return language{prose: true}
	}
	return language{hash: hashExt[ext], slash: slashExt[ext], indent: indentExt[ext]}
}

// keywords are reserved words across the languages the generic lexer
// reads. It only matters to the rename check — "if" renamed to "while"
// is not a rename — so one shared list that is a little too wide is
// safer than one per language that is a little too narrow.
var keywords = set("if", "else", "elif", "for", "while", "do", "return", "break", "continue", "switch", "case",
	"default", "def", "class", "fn", "func", "function", "let", "var", "const", "mut", "static", "pub", "public",
	"private", "protected", "import", "from", "as", "export", "package", "try", "except", "catch", "finally",
	"raise", "throw", "throws", "new", "delete", "in", "is", "not", "and", "or", "with", "yield", "await", "async",
	"lambda", "pass", "global", "nonlocal", "assert", "del", "true", "false", "True", "False", "None", "null",
	"nil", "undefined", "this", "self", "super", "struct", "enum", "impl", "trait", "match", "loop", "where",
	"type", "interface", "extends", "implements", "void", "int", "float", "bool", "string", "char", "unsigned",
	"long", "short", "double", "typeof", "instanceof", "goto", "then", "fi", "end", "begin", "unless", "until",
	"elsif", "require", "include", "use", "mod", "crate", "ref", "move", "unsafe", "extern", "sizeof")

// goPredeclared are Go's predeclared identifiers: the scanner reads them
// as plain identifiers, but true → false is no rename.
var goPredeclared = set("true", "false", "nil", "iota", "append", "cap", "clear", "close", "complex", "copy",
	"delete", "imag", "len", "make", "max", "min", "new", "panic", "print", "println", "real", "recover",
	"any", "bool", "byte", "comparable", "complex64", "complex128", "error", "float32", "float64", "int",
	"int8", "int16", "int32", "int64", "rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr")

func (l language) keyword(s string) bool {
	if l.goSrc {
		return goPredeclared[s]
	}
	return keywords[s]
}

// tokens lexes src. Unterminated strings and comments run to the end of
// the input rather than failing: a hunk can begin or end inside one.
func (l language) tokens(src string) []tok {
	if l.goSrc {
		return goTokens(src)
	}
	var out []tok
	atLine := true
	for i := 0; i < len(src); {
		c := src[i]
		if c == '\n' {
			atLine = true
			i++
			continue
		}
		if atLine && l.indent {
			j := i
			for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
				j++
			}
			// A blank line's indentation means nothing even where
			// indentation does.
			if j < len(src) && src[j] != '\n' && src[j] != '\r' {
				out = append(out, tok{'n', src[i:j]})
			}
			atLine = false
			i = j
			continue
		}
		atLine = false
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
		case l.hash && c == '#', l.slash && strings.HasPrefix(src[i:], "//"):
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				j = len(src) - i
			}
			out = append(out, tok{'c', strings.TrimRight(src[i:i+j], " \t\r")})
			i += j
		case l.slash && strings.HasPrefix(src[i:], "/*"):
			j := strings.Index(src[i+2:], "*/")
			end := len(src)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			out = append(out, tok{'c', src[i:end]})
			i = end
		case c == '"' || c == '\'' || c == '`':
			end := stringEnd(src, i)
			out = append(out, tok{'o', src[i:end]})
			i = end
		case isIdentStart(c):
			j := i + 1
			for j < len(src) && isIdentPart(src[j]) {
				j++
			}
			out = append(out, tok{'i', src[i:j]})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < len(src) && (isIdentPart(src[j]) || src[j] == '.') {
				j++
			}
			out = append(out, tok{'o', src[i:j]})
			i = j
		default:
			out = append(out, tok{'o', src[i : i+1]})
			i++
		}
	}
	return out
}

// stringEnd is the index just past the string literal starting at i,
// tripled quotes (Python docstrings) included.
func stringEnd(src string, i int) int {
	q := src[i]
	if strings.HasPrefix(src[i:], strings.Repeat(string(q), 3)) {
		if j := strings.Index(src[i+3:], strings.Repeat(string(q), 3)); j >= 0 {
			return i + 3 + j + 3
		}
		return len(src)
	}
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case q:
			return j + 1
		case '\n':
			if q != '`' {
				return j
			}
		}
	}
	return len(src)
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isIdentPart(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

// goTokens lexes Go with the standard library's scanner. The semicolons
// it inserts at line ends are dropped: they follow the layout, and a
// reflow that moves them is exactly what Format is meant to accept.
func goTokens(src string) []tok {
	var s scanner.Scanner
	fs := token.NewFileSet()
	f := fs.AddFile("", fs.Base(), len(src))
	s.Init(f, []byte(src), func(token.Position, string) {}, scanner.ScanComments)
	var out []tok
	for {
		_, t, lit := s.Scan()
		switch {
		case t == token.EOF:
			return out
		case t == token.SEMICOLON && lit == "\n":
		case t == token.COMMENT:
			out = append(out, tok{'c', strings.TrimRight(lit, " \t\r\n")})
		case t == token.IDENT:
			out = append(out, tok{'i', lit})
		case lit != "":
			out = append(out, tok{'o', lit})
		default:
			out = append(out, tok{'o', t.String()})
		}
	}
}
