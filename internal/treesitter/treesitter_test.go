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

// A decorated function is one declaration. The Python query used to
// name it twice — once as a function_definition and once as the
// definition inside the decorated_definition wrapping it — so every
// decorated function went into the map twice.
func TestNoDeclarationIsCapturedTwice(t *testing.T) {
	cases := []struct{ path, src string }{
		{"a.py", "@app.route(\"/\")\ndef handler(req):\n    return req\n\n@dataclass\nclass Thing:\n    pass\n"},
		{"a.ts", "export class Store { put(k: string) {} }\nexport function load(): Store { return new Store() }\n"},
		{"a.rs", "pub struct Store { n: usize }\npub fn load() -> Store { Store { n: 0 } }\n"},
	}
	for _, c := range cases {
		defs, _, ok := Symbols(c.path, []byte(c.src))
		if !ok {
			t.Fatalf("%s: not parsed", c.path)
		}
		seen := map[repomap.Symbol]bool{}
		for _, d := range defs {
			if seen[d] {
				t.Errorf("%s: %+v captured twice (all: %+v)", c.path, d, defs)
			}
			seen[d] = true
		}
	}
	// And the decorated function is still found.
	defs, _, _ := Symbols("a.py", []byte("@app.route(\"/\")\ndef handler(req):\n    return req\n"))
	if got := names(defs); got["handler"] != "func" {
		t.Fatalf("decorated function lost: %v", got)
	}
}

// Every extension of one language shares a single grammar value, so the
// grammar and its query are loaded and compiled once rather than once
// per extension.
func TestOneGrammarPerLanguage(t *testing.T) {
	for _, group := range [][]string{
		{".js", ".jsx", ".mjs", ".cjs"},
		{".ts", ".mts", ".cts"},
		{".c", ".h"},
	} {
		first := languages[group[0]]
		if first == nil {
			t.Fatalf("no grammar for %s", group[0])
		}
		for _, ext := range group[1:] {
			if languages[ext] != first {
				t.Errorf("%s and %s do not share a grammar value", group[0], ext)
			}
		}
	}
	// And the queries still compile: a typo in a pattern is rejected by
	// the grammar, not silently ignored.
	seen := map[*language]bool{}
	for ext, l := range languages {
		if seen[l] {
			continue
		}
		seen[l] = true
		if err := l.load(); err != nil {
			t.Errorf("%s (%s): %v", ext, l.name, err)
		}
	}
}

// Parser is what the composition root hands to the repository map, so a
// build with the tag has one and a build without it does not.
func TestParserIsTheRepomapHook(t *testing.T) {
	var sym repomap.Parser = Parser()
	if sym == nil {
		t.Fatal("Parser() returned nil in a treesitter build")
	}
	defs, _, ok := sym("a.py", []byte("def f(): pass\n"))
	if !ok || len(defs) != 1 || defs[0].Name != "f" {
		t.Fatalf("through the hook: ok=%v defs=%+v", ok, defs)
	}
}

// Spans are the top-level nodes, one per declaration, with their lines;
// a file with a syntax error gives none rather than spans that would put
// one broken declaration's halves in two regions.
func TestSpans(t *testing.T) {
	src := "def a():\n    return 1\n\n\ndef b():\n    x = 2\n    return x\n"
	spans, ok := Spans("m.py", []byte(src))
	if !ok || len(spans) != 2 || spans[0] != [2]int{1, 2} || spans[1] != [2]int{5, 7} {
		t.Fatalf("spans = %v ok=%v", spans, ok)
	}
	if _, ok := Spans("m.py", []byte("def a(:\n")); ok {
		t.Fatal("a file that does not parse gave spans")
	}
	if _, ok := Spans("m.go", []byte("package m\n")); ok {
		t.Fatal("Go is not handled here")
	}
}
