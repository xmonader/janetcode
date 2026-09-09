//go:build treesitter

package treesitter

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/repomap"
)

func names(defs []repomap.Symbol) map[string]string {
	out := map[string]string{}
	for _, d := range defs {
		out[d.Name] = d.Kind
	}
	return out
}

func TestSymbolsPerLanguage(t *testing.T) {
	cases := []struct {
		path string
		src  string
		want map[string]string
	}{
		{"a.py", `
class Store:
    def put(self, k): ...

def load(path):
    return Store()
`, map[string]string{"Store": "class", "put": "func", "load": "func"}},
		{"a.ts", `
export interface Config { name: string }
type Alias = Config
export class Store { put(k: string) {} }
export function load(): Store { return new Store() }
const arrow = () => 1
`, map[string]string{"Config": "interface", "Alias": "type", "Store": "class", "put": "method", "load": "func", "arrow": "func"}},
		{"a.rs", `
pub struct Store { n: usize }
pub trait Put { fn put(&self); }
pub fn load() -> Store { Store { n: 0 } }
mod inner {}
`, map[string]string{"Store": "type", "Put": "trait", "put": "func", "load": "func", "inner": "module"}},
		{"a.c", `
typedef struct Store { int n; } Store_t;
int load(const char *path) { return 0; }
`, map[string]string{"Store": "type", "Store_t": "type", "load": "func"}},
	}
	for _, c := range cases {
		defs, refs, ok := Symbols(c.path, []byte(c.src))
		if !ok {
			t.Fatalf("%s: not parsed", c.path)
		}
		got := names(defs)
		for name, kind := range c.want {
			if got[name] != kind {
				t.Fatalf("%s: %s is %q, want %q (all: %v)", c.path, name, got[name], kind, got)
			}
		}
		if len(refs) == 0 {
			t.Fatalf("%s: no identifiers", c.path)
		}
	}
}

// The point of a grammar over a regular expression: a name written in a
// comment or a string is not a declaration and not a reference.
func TestCommentsAndStringsAreNotCode(t *testing.T) {
	src := `
# def ghost(x): pass
"""
class PhantomClass: pass
"""
def real():
    return "AnotherPhantom"
`
	defs, refs, ok := Symbols("a.py", []byte(src))
	if !ok {
		t.Fatal("not parsed")
	}
	got := names(defs)
	if _, bad := got["ghost"]; bad {
		t.Fatalf("a commented-out def was indexed: %v", got)
	}
	if _, bad := got["PhantomClass"]; bad {
		t.Fatalf("a class inside a docstring was indexed: %v", got)
	}
	if got["real"] != "func" {
		t.Fatalf("real not found: %v", got)
	}
	for _, r := range refs {
		if r == "AnotherPhantom" || r == "PhantomClass" {
			t.Fatalf("a name inside a string counted as a reference: %v", refs)
		}
	}
}

func TestUnsupportedAndUnparsable(t *testing.T) {
	if Supported("a.zig") || Supported("a.go") {
		t.Fatal("zig and go are not handled here")
	}
	if !Supported("a.PY") {
		t.Fatal("extension match should ignore case")
	}
	if _, _, ok := Symbols("a.txt", []byte("hello")); ok {
		t.Fatal("unknown extension parsed")
	}
	big := strings.Repeat("x = 1\n", MaxBytes)
	if _, _, ok := Symbols("a.py", []byte(big)); ok {
		t.Fatal("oversized file parsed")
	}
	// Nonsense in a C file fails at the root and falls back.
	if _, _, ok := Symbols("a.c", []byte("@@@ not c @@@")); ok {
		t.Fatal("unparsable source accepted")
	}
}

// The parser is registered with the repository map by init, so building
// with the tag is all it takes.
func TestRegisteredWithRepomap(t *testing.T) {
	if repomap.SymbolParser == nil {
		t.Fatal("repomap.SymbolParser was not registered")
	}
	defs, _, ok := repomap.SymbolParser("a.py", []byte("def f(): pass\n"))
	if !ok || len(defs) != 1 || defs[0].Name != "f" {
		t.Fatalf("through the hook: ok=%v defs=%+v", ok, defs)
	}
}
