package lsp

import (
	"context"
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
	name    string
	bin     string
	files   map[string]string
	file    string // where to ask
	symbol  string // what to ask about
	defIn   string // the file the definition should land in
	outline []string
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

	// References and hover are allowed to come back empty on a server
	// that does not implement them, but never to error.
	if _, err := m.References(ctx, c.file, Point{Symbol: c.symbol}); err != nil {
		t.Fatalf("%s: references: %v", c.name, err)
	}
	if _, err := m.Hover(ctx, c.file, Point{Symbol: c.symbol}); err != nil {
		t.Fatalf("%s: hover: %v", c.name, err)
	}
}

func TestPyright(t *testing.T) {
	run(t, langCase{
		name: "pyright", bin: "pyright-langserver",
		files: map[string]string{
			"store.py": "class Store:\n    def put(self, k):\n        return k\n\n\ndef load():\n    return Store()\n",
			"main.py":  "from store import load\n\nc = load()\n",
		},
		file: "main.py", symbol: "load", defIn: "store.py",
		outline: []string{"c"},
	})
}

func TestTypeScriptServer(t *testing.T) {
	run(t, langCase{
		name: "typescript", bin: "typescript-language-server",
		files: map[string]string{
			"tsconfig.json": `{"compilerOptions":{"target":"es2020","moduleResolution":"node"}}`,
			"store.ts":      "export class Store { put(k: string) {} }\nexport function load(): Store { return new Store() }\n",
			"main.ts":       "import { load } from './store'\nconst c = load()\n",
		},
		file: "store.ts", symbol: "Store", defIn: "store.ts",
		outline: []string{"Store", "load"},
	})
}

func TestRustAnalyzer(t *testing.T) {
	run(t, langCase{
		name: "rust", bin: "rust-analyzer",
		files: map[string]string{
			"Cargo.toml":  "[package]\nname = \"x\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
			"src/main.rs": "pub struct Store { pub n: usize }\n\npub fn load() -> Store { Store { n: 0 } }\n\nfn main() { let _ = load(); }\n",
		},
		file: "src/main.rs", symbol: "Store", defIn: "main.rs",
		outline: []string{"Store", "load"},
	})
}

func TestClangd(t *testing.T) {
	run(t, langCase{
		name: "c", bin: "clangd",
		files: map[string]string{
			"store.h": "typedef struct Store { int n; } Store;\nint load(void);\n",
			"main.c":  "#include \"store.h\"\n\nint load(void) { return 0; }\n\nint main(void) { return load(); }\n",
		},
		file: "main.c", symbol: "load", defIn: "main.c|store.h",
		outline: []string{"load", "main"},
	})
}
