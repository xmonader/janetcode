//go:build !treesitter

// Package treesitter is empty in the default build: the grammars are C, so
// compiling them in needs cgo and a C toolchain, and the default build
// stays pure Go so it cross-compiles. Build with `-tags treesitter` to get
// the real parser; the repository map then uses grammars instead of
// regular expressions for the languages it covers.
package treesitter

import "github.com/xmonader/janetcode/internal/repomap"

// Parser returns the grammar-backed symbol parser. Without the build tag
// there is none, so the repository map keeps its built-in patterns.
func Parser() repomap.Parser { return nil }

// Supported reports whether a grammar is compiled in for the file. Without
// the build tag, none is.
func Supported(string) bool { return false }

// Spans returns the top-level node spans of a file. Without the build
// tag there is no grammar, so the caller falls back to line ranges.
func Spans(string, []byte) ([][2]int, bool) { return nil, false }

// Def is one named definition: the node that declares Name, as a byte
// range of the source (see Definitions).
type Def struct {
	Name       string
	Kind       string
	Start, End int
	Line       int
}

// Definitions lists the named definitions of a file by syntax node. Without
// the build tag there is no grammar, so there are none.
func Definitions(string, []byte) ([]Def, bool) { return nil, false }

// SyntaxErrors counts the ERROR and missing nodes of a parse. Without the
// build tag it cannot.
func SyntaxErrors(string, []byte) (int, bool) { return 0, false }
