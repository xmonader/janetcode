//go:build treesitter

// Package treesitter parses source with real grammars instead of regular
// expressions, so the repository map names what a file actually declares.
//
// It is behind a build tag because the grammars are C: building it needs
// cgo and a C toolchain, and the default build stays pure Go so it still
// cross-compiles. Build with `-tags treesitter` (see the Makefile's
// build-treesitter target) to compile it in; Parser then returns the
// grammar-backed parser for the composition root to hand to the
// repository map.
//
// Go is deliberately not handled here. The standard library's parser is
// more accurate than a grammar for Go and is always available, so the map
// keeps using it.
package treesitter

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspy "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/xmonader/janetcode/internal/repomap"
)

// Parser returns the grammar-backed symbol parser, or nil in a build
// without the grammars compiled in. The composition root passes it to the
// repository map: nothing here registers itself.
func Parser() repomap.Parser { return Symbols }

// MaxBytes is the largest file this parses. A grammar builds a full tree
// in memory, and a generated or minified file is neither cheap nor worth
// mapping.
const MaxBytes = 1 << 20

// language is one grammar and the query that names its declarations.
type language struct {
	name  string
	fn    func() unsafe.Pointer
	query string

	once sync.Once
	lang *ts.Language
	q    *ts.Query
	err  error
}

// The queries capture the name of each declaration and tag it with the
// capture's own name, which becomes the kind shown in the map.
//
// One value per grammar, shared by every extension that grammar covers:
// loading a grammar and compiling its query is not free, and .js and
// .mjs are the same language. The map is read-only after init and each
// value guards its own one-time load, so Symbols is safe to call from
// several goroutines.
var (
	pythonLang = &language{name: "python", fn: tspy.Language, query: pyQuery}
	jsLang     = &language{name: "javascript", fn: tsjs.Language, query: jsQuery}
	tsLang     = &language{name: "typescript", fn: tsts.LanguageTypescript, query: tsQuery}
	tsxLang    = &language{name: "tsx", fn: tsts.LanguageTSX, query: tsQuery}
	rustLang   = &language{name: "rust", fn: tsrust.Language, query: rustQuery}
	cLang      = &language{name: "c", fn: tsc.Language, query: cQuery}
)

var languages = map[string]*language{
	".py":  pythonLang,
	".js":  jsLang,
	".jsx": jsLang,
	".mjs": jsLang,
	".cjs": jsLang,
	".ts":  tsLang,
	".mts": tsLang,
	".cts": tsLang,
	".tsx": tsxLang,
	".rs":  rustLang,
	".c":   cLang,
	".h":   cLang,
}

// A decorated Python function needs no pattern of its own: the
// function_definition inside it matches the first pattern already, and
// naming it twice put every decorated function in the map twice.
const pyQuery = `
(function_definition name: (identifier) @func)
(class_definition name: (identifier) @class)
`

const rustQuery = `
(function_item name: (identifier) @func)
(function_signature_item name: (identifier) @func)
(struct_item name: (type_identifier) @type)
(enum_item name: (type_identifier) @type)
(union_item name: (type_identifier) @type)
(trait_item name: (type_identifier) @trait)
(mod_item name: (identifier) @module)
(const_item name: (identifier) @const)
(static_item name: (identifier) @var)
(type_item name: (type_identifier) @type)
(macro_definition name: (identifier) @macro)
`

// The shared part of the JavaScript and TypeScript queries. Classes are
// not in it: JavaScript names a class with an identifier and TypeScript
// with a type_identifier, and a pattern naming the wrong one is rejected
// by the grammar rather than silently ignored.
const jsCommon = `
(function_declaration name: (identifier) @func)
(generator_function_declaration name: (identifier) @func)
(method_definition name: (property_identifier) @method)
(variable_declarator name: (identifier) @func value: (arrow_function))
(variable_declarator name: (identifier) @func value: (function_expression))
`

const jsQuery = jsCommon + `
(class_declaration name: (identifier) @class)
`

const tsQuery = jsCommon + `
(class_declaration name: (type_identifier) @class)
(abstract_class_declaration name: (type_identifier) @class)
(interface_declaration name: (type_identifier) @interface)
(type_alias_declaration name: (type_identifier) @type)
(enum_declaration name: (identifier) @enum)
(function_signature name: (identifier) @func)
`

const cQuery = `
(function_definition declarator: (function_declarator declarator: (identifier) @func))
(declaration declarator: (function_declarator declarator: (identifier) @func))
(struct_specifier name: (type_identifier) @type)
(union_specifier name: (type_identifier) @type)
(enum_specifier name: (type_identifier) @type)
(type_definition declarator: (type_identifier) @type)
`

// Supported reports whether a grammar is compiled in for the file.
func Supported(path string) bool { return languages[strings.ToLower(filepath.Ext(path))] != nil }

// load compiles the grammar and its query once, and remembers a failure so
// a broken query is not recompiled for every file.
func (l *language) load() error {
	l.once.Do(func() {
		l.lang = ts.NewLanguage(l.fn())
		q, err := ts.NewQuery(l.lang, l.query)
		if err != nil {
			l.err = fmt.Errorf("%s query: %w", l.name, err)
			return
		}
		l.q = q
	})
	return l.err
}

// Symbols parses src and returns what it declares and the identifiers it
// mentions. ok is false when no grammar handles the file, the file is too
// large, or the source does not parse — the caller then falls back.
func Symbols(path string, src []byte) ([]repomap.Symbol, []string, bool) {
	l := languages[strings.ToLower(filepath.Ext(path))]
	if l == nil || len(src) > MaxBytes {
		return nil, nil, false
	}
	if err := l.load(); err != nil {
		return nil, nil, false
	}
	parser := ts.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(l.lang); err != nil {
		return nil, nil, false
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, nil, false
	}
	defer tree.Close()
	root := tree.RootNode()
	var defs []repomap.Symbol
	cursor := ts.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(l.q, root, src)
	names := l.q.CaptureNames()
	for m := matches.Next(); m != nil; m = matches.Next() {
		for _, cap := range m.Captures {
			kind := "def"
			if int(cap.Index) < len(names) {
				kind = names[cap.Index]
			}
			defs = append(defs, repomap.Symbol{
				Name: cap.Node.Utf8Text(src),
				Kind: kind,
				Line: int(cap.Node.StartPosition().Row) + 1,
			})
		}
	}

	// A file with a few error nodes still names most of what it declares,
	// and a grammar's answer is better than a regular expression's even
	// then. One that yields nothing and does not parse is a different
	// matter: the caller falls back rather than indexing an empty file.
	if len(defs) == 0 && root.HasError() {
		return nil, nil, false
	}

	// Identifiers, from the tree rather than from the text: a name in a
	// comment or a string is not a reference, and counting one links files
	// that have nothing to do with each other.
	refs := identifiers(root, src)
	return defs, refs, true
}

// identifiers walks the tree collecting identifier-like leaves.
func identifiers(root *ts.Node, src []byte) []string {
	var out []string
	cursor := root.Walk()
	defer cursor.Close()
	visited := false
	for {
		if !visited {
			n := cursor.Node()
			if n.ChildCount() == 0 && isIdentifier(n.Kind()) {
				out = append(out, n.Utf8Text(src))
			}
		}
		if !visited && cursor.GotoFirstChild() {
			continue
		}
		if cursor.GotoNextSibling() {
			visited = false
			continue
		}
		if !cursor.GotoParent() {
			return out
		}
		visited = true
	}
}

// isIdentifier covers the identifier node kinds the grammars here use.
func isIdentifier(kind string) bool {
	switch kind {
	case "identifier", "type_identifier", "property_identifier", "field_identifier",
		"shorthand_property_identifier", "shorthand_property_identifier_pattern",
		"statement_identifier", "namespace_identifier", "primitive_type":
		return true
	}
	return false
}

// Spans returns the line span (1-based, inclusive) of every top-level
// node of the file: its declarations, statements and comments. A merge
// that asks whether two edits touch the same declaration reads them. ok
// is false when no grammar handles the file, it is too large, or it does
// not parse cleanly — a span list from a tree with errors in it would
// put two edits in different "declarations" that are really one.
func Spans(path string, src []byte) ([][2]int, bool) {
	l := languages[strings.ToLower(filepath.Ext(path))]
	if l == nil || len(src) > MaxBytes {
		return nil, false
	}
	if err := l.load(); err != nil {
		return nil, false
	}
	parser := ts.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(l.lang); err != nil {
		return nil, false
	}
	tree := parser.Parse(src, nil)
	if tree == nil {
		return nil, false
	}
	defer tree.Close()
	root := tree.RootNode()
	if root.HasError() {
		return nil, false
	}
	var out [][2]int
	for i := uint(0); i < root.NamedChildCount(); i++ {
		n := root.NamedChild(i)
		if n == nil {
			continue
		}
		out = append(out, [2]int{int(n.StartPosition().Row) + 1, int(n.EndPosition().Row) + 1})
	}
	return out, true
}
