//go:build !treesitter

package treesitter

import (
	"testing"

	"github.com/xmonader/janetcode/internal/repomap"
)

// The default build has no grammars, and the contract the composition
// root relies on is that it says so rather than pretending: Parser is
// nil, so the repository map keeps its own patterns, and Supported is
// false for every path, including the ones the tagged build handles.
func TestStubOffersNoParser(t *testing.T) {
	var p repomap.Parser = Parser()
	if p != nil {
		t.Fatal("a build without the tag must have no grammar parser")
	}
	for _, path := range []string{"a.py", "a.ts", "a.tsx", "a.rs", "a.c", "a.h", "a.go", ""} {
		if Supported(path) {
			t.Errorf("Supported(%q) = true without the grammars compiled in", path)
		}
	}
}
