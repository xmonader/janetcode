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
