package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The query surface is defined against the protocol, but servers differ
// in which shapes they answer with — one Location, a list, or a list of
// LocationLinks; a flat symbol list or a nested tree; markup or plain
// hover. Each of these ran against the real server, so a decoder that
// only ever saw gopls does not pass by accident.

type langCase struct {
	name   string
	bin    string
	files  map[string]string
	file   string // where to ask
	symbol string // what to ask about
	defIn  string // the file the definition should land in, "a|b" for either
	// outline are names the file's own symbol list must contain.
	outline []string
	// refSymbol is used across the whole query surface: every use of it
	// must be found, its type must be described, and a workspace search
	// must locate it. Empty skips those, for a server that does not
	// implement them.
	refSymbol string
	// minRefs is how many uses of refSymbol there are, counting the
	// declaration; a server returning fewer is missing some.
	minRefs int
	// hoverHas is a string the type description must contain.
	hoverHas string
	// searchable is false for a server with no workspace symbol support.
	searchable bool
	// compileDB writes a compile_commands.json naming these files.
	// clangd indexes across files only when it has one, which a real C
	// project provides and a bare directory does not.
	compileDB []string
}

func run(t *testing.T, c langCase) {
	t.Helper()
	if _, err := exec.LookPath(c.bin); err != nil {
		t.Skipf("%s not installed", c.bin)
	}
	root := t.TempDir()
	for name, body := range c.files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.compileDB) > 0 {
		var entries []string
		for _, f := range c.compileDB {
			entries = append(entries, fmt.Sprintf(`{"directory":%q,"file":%q,"command":"cc -c %s"}`, root, filepath.Join(root, f), f))
		}
		if err := os.WriteFile(filepath.Join(root, "compile_commands.json"), []byte("["+strings.Join(entries, ",")+"]"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := New(root, Config{Timeout: 90})
	defer m.Close()
	if m.ServerFor(c.file) == "" {
		t.Fatalf("no server handles %s", c.file)
	}
	ctx := context.Background()

	defs, err := m.Definition(ctx, c.file, Point{Symbol: c.symbol})
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if len(defs) == 0 {
		t.Fatalf("%s: no definition for %s", c.name, c.symbol)
	}
	if c.defIn != "" {
		var found bool
		for _, d := range defs {
			// A C compiler resolves to the declaration in the header, which
			// is as correct as the definition; either satisfies the check.
			for _, want := range strings.Split(c.defIn, "|") {
				if strings.Contains(d.Path, want) {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("%s: %s resolved to %+v, want one of %s", c.name, c.symbol, defs, c.defIn)
		}
	}
	if defs[0].Line < 1 {
		t.Fatalf("%s: line %d is not 1-based", c.name, defs[0].Line)
	}

	// An outline must name what the file declares, whichever shape the
	// server answers in.
	syms, err := m.Outline(ctx, c.file)
	if err != nil {
		t.Fatalf("outline: %v", err)
	}
	var names []string
	for _, s := range syms {
		names = append(names, s.Name)
	}
	for _, want := range c.outline {
		if !contains(names, want) {
			t.Fatalf("%s: outline %v is missing %q", c.name, names, want)
		}
	}

	if c.refSymbol == "" {
		return
	}

	// References: every use, not just the one the query pointed at. This
	// is the whole reason to ask a server rather than grep.
	refs, err := m.References(ctx, c.file, Point{Symbol: c.refSymbol})
	if err != nil {
		t.Fatalf("%s: references: %v", c.name, err)
	}
	if len(refs) < c.minRefs {
		t.Fatalf("%s: %d references to %s, want at least %d: %+v", c.name, len(refs), c.refSymbol, c.minRefs, refs)
	}
	files := map[string]bool{}
	for _, r := range refs {
		if r.Line < 1 || r.Col < 1 {
			t.Fatalf("%s: reference %+v is not 1-based", c.name, r)
		}
		files[filepath.Base(r.Path)] = true
	}
	if len(files) < 2 {
		t.Fatalf("%s: references to %s all landed in one file (%v); a use from elsewhere was missed", c.name, c.refSymbol, files)
	}

	// Hover: what the thing is, which is what makes it worth asking.
	hov, err := m.Hover(ctx, c.file, Point{Symbol: c.refSymbol})
	if err != nil {
		t.Fatalf("%s: hover: %v", c.name, err)
	}
	if c.hoverHas != "" && !strings.Contains(hov, c.hoverHas) {
		t.Fatalf("%s: hover for %s = %q, want it to mention %q", c.name, c.refSymbol, hov, c.hoverHas)
	}

	// Workspace search: find it by name, without saying where it lives.
	if c.searchable {
		syms, err := m.WorkspaceSymbols(ctx, c.refSymbol, c.file)
		if err != nil {
			t.Fatalf("%s: workspace search: %v", c.name, err)
		}
		var found bool
		for _, s := range syms {
			if strings.Contains(s.Name, c.refSymbol) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: a search for %s found %+v", c.name, c.refSymbol, syms)
		}
	}

	// The tool renders all of it for the model.
	tl := SymbolTool{M: m}
	for _, action := range []string{"references", "type", "outline"} {
		out, err := tl.Run(ctx, []byte(`{"action":"`+action+`","path":"`+c.file+`","symbol":"`+c.refSymbol+`"}`))
		if err != nil {
			t.Fatalf("%s: tool %s: %v", c.name, action, err)
		}
		if strings.TrimSpace(out) == "" {
			t.Fatalf("%s: tool %s rendered nothing", c.name, action)
		}
	}
}

func TestPyright(t *testing.T) {
	run(t, langCase{
		name: "pyright", bin: "pyright-langserver",
		files: map[string]string{
			"store.py": "class Store:\n    def put(self, k):\n        return k\n\n\ndef load():\n    return Store()\n",
			"main.py":  "from store import Store, load\n\nc: Store = load()\n",
		},
		file: "store.py", symbol: "load", defIn: "store.py",
		outline:   []string{"Store", "load"},
		refSymbol: "Store", minRefs: 2, hoverHas: "Store", searchable: true,
	})
}

func TestTypeScriptServer(t *testing.T) {
	run(t, langCase{
		name: "typescript", bin: "typescript-language-server",
		files: map[string]string{
			"tsconfig.json": `{"compilerOptions":{"target":"es2020","moduleResolution":"node"}}`,
			"store.ts":      "export class Store { put(k: string) {} }\nexport function load(): Store { return new Store() }\n",
			"main.ts":       "import { load } from './store'\nconst c = load()\nconst d = load()\n",
		},
		file: "store.ts", symbol: "Store", defIn: "store.ts",
		outline:   []string{"Store", "load"},
		refSymbol: "load", minRefs: 2, hoverHas: "load", searchable: true,
	})
}

func TestRustAnalyzer(t *testing.T) {
	run(t, langCase{
		name: "rust", bin: "rust-analyzer",
		files: map[string]string{
			"Cargo.toml":  "[package]\nname = \"x\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
			"src/lib.rs":  "pub struct Store { pub n: usize }\n\npub fn load() -> Store { Store { n: 0 } }\n",
			"src/main.rs": "use x::{load, Store};\n\nfn main() { let s: Store = load(); let _ = s.n; }\n",
		},
		file: "src/main.rs", symbol: "Store", defIn: "lib.rs",
		outline:   []string{"main"},
		refSymbol: "Store", minRefs: 2, hoverHas: "Store",
	})
}

func TestClangd(t *testing.T) {
	run(t, langCase{
		name: "c", bin: "clangd",
		files: map[string]string{
			"store.h": "typedef struct Store { int n; } Store;\nint load(void);\n",
			"main.c":  "#include \"store.h\"\n\nint load(void) { return 0; }\n\nint main(void) { return load(); }\n",
			"use.c":   "#include \"store.h\"\n\nint twice(void) { return load() + load(); }\n",
		},
		file: "main.c", symbol: "load", defIn: "main.c|store.h",
		compileDB: []string{"main.c", "use.c"},
		outline:   []string{"load", "main"},
		refSymbol: "load", minRefs: 2, hoverHas: "load", searchable: true,
	})
}
