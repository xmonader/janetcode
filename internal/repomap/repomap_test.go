package repomap

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.22\n",
		"store/store.go":      "package store\n\ntype Store struct{ n int }\n\nfunc Open(path string) *Store { return &Store{} }\n\nfunc (s *Store) Put(k, v string) error { return nil }\n\nfunc (s *Store) Get(k string) (string, bool) { return \"\", false }\n",
		"api/handler.go":      "package api\n\nimport \"example.com/m/store\"\n\ntype Handler struct{ st *store.Store }\n\nfunc NewHandler(st *store.Store) *Handler { return &Handler{st: st} }\n\nfunc (h *Handler) Serve(k string) { h.st.Get(k); h.st.Put(k, k) }\n",
		"cmd/main.go":         "package main\n\nimport (\n\t\"example.com/m/api\"\n\t\"example.com/m/store\"\n)\n\nfunc main() { api.NewHandler(store.Open(\"x\")).Serve(\"k\") }\n",
		"scripts/tool.py":     "import os\n\nclass Loader:\n    def load(self, path):\n        return open(path).read()\n\ndef helper(x):\n    return Loader().load(x)\n",
		"web/app.ts":          "export function render(node: Node) { return node }\nexport class View { draw() { render(this) } }\nconst helper = 1\n",
		"store/store_test.go": "package store\n\nimport \"testing\"\n\nfunc TestOpen(t *testing.T) { Open(\"x\") }\n",
		"README.md":           "# not source\n",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestBuildLinksDefinersAndReferencers(t *testing.T) {
	root := fixture(t)
	ix, err := Build(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Files) != 6 {
		t.Fatalf("indexed %d files: %v", len(ix.Files), paths(ix.Files))
	}
	st := ix.Files[ix.byPath["store/store.go"]]
	var names []string
	for _, d := range st.Defs {
		names = append(names, d.Kind+":"+d.Name)
	}
	joined := strings.Join(names, " ")
	for _, want := range []string{"type:Store", "func:Open", "method:Store.Put", "method:Store.Get"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}
	// Store is referenced from handler.go and main.go: it sorts first.
	if st.Defs[0].Name != "Store" || st.Defs[0].Refs == 0 {
		t.Fatalf("defs by refs = %v", names)
	}
	py := ix.Files[ix.byPath["scripts/tool.py"]]
	if len(py.Defs) != 3 || py.Defs[0].Name == "" {
		t.Fatalf("python defs = %+v", py.Defs)
	}
	r, ok := ix.BlastRadius("Store", false)
	if !ok || strings.Join(r.Definers, ",") != "store/store.go" || len(r.Referencers) != 1 || r.Referencers[0].Path != "api/handler.go" {
		t.Fatalf("radius = %+v", r)
	}
	r, ok = ix.BlastRadius("Open", false)
	if !ok || len(r.Referencers) != 2 || r.Referencers[0].Path != "cmd/main.go" || r.Referencers[1].Path != "store/store_test.go" {
		t.Fatalf("Open radius = %+v", r)
	}
	r, ok = ix.BlastRadius("Put", false)
	if !ok || len(r.Referencers) != 1 || r.Referencers[0].Path != "api/handler.go" {
		t.Fatalf("method radius = %+v", r)
	}
	r, ok = ix.BlastRadius("store/store.go", true)
	if !ok || len(r.Referencers) != 3 { // handler, main, test
		t.Fatalf("file radius = %+v", r)
	}
	if _, ok := ix.BlastRadius("Nope", false); ok {
		t.Fatal("unknown symbol resolved")
	}
	// "helper" is defined in two files (py, ts): both are definers.
	if r, ok := ix.BlastRadius("helper", false); !ok || len(r.Definers) != 2 {
		t.Fatalf("helper radius = %+v", r)
	}
}

func TestRankIsPersonalisedByRequest(t *testing.T) {
	root := fixture(t)
	ix, err := Build(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	// Without a request the most depended-on file wins.
	plain := ix.Rank("", nil)
	if plain[0].File.Path != "store/store.go" {
		t.Fatalf("unpersonalised top = %s", plain[0].File.Path)
	}
	// Mentioning the handler by name pulls it to the top, and says why.
	ranked := ix.Rank("add logging to the Serve method in the handler", nil)
	if ranked[0].File.Path != "api/handler.go" || strings.Join(ranked[0].Why, ",") != "path,symbol Handler.Serve" {
		t.Fatalf("top = %s why=%v", ranked[0].File.Path, ranked[0].Why)
	}
	// A path mention and a changed file each count.
	ranked = ix.Rank("look at scripts/tool.py", []string{"web/app.ts"})
	var whyPy, whyTs string
	for _, r := range ranked {
		switch r.File.Path {
		case "scripts/tool.py":
			whyPy = strings.Join(r.Why, ",")
		case "web/app.ts":
			whyTs = strings.Join(r.Why, ",")
		}
	}
	if whyPy != "path" || whyTs != "changed" {
		t.Fatalf("why py=%q ts=%q", whyPy, whyTs)
	}
	// Stems match: "handlers" finds NewHandler; short names never count.
	ranked = ix.Rank("the handlers need validation", nil)
	if ranked[0].File.Path != "api/handler.go" || !strings.Contains(strings.Join(ranked[0].Why, ","), "symbol") {
		t.Fatalf("stem top = %s why=%v", ranked[0].File.Path, ranked[0].Why)
	}
	if r := ix.Rank("fix the main and load paths", nil); len(r[0].Why) > 0 && r[0].Why[0] != "changed" {
		for _, x := range r {
			for _, w := range x.Why {
				if strings.HasPrefix(w, "symbol main") || strings.HasPrefix(w, "symbol load") {
					t.Fatalf("short symbol matched: %v", x.Why)
				}
			}
		}
	}
	// CamelCase mentions match by part.
	ranked = ix.Rank("NewHandler should validate", nil)
	if ranked[0].File.Path != "api/handler.go" {
		t.Fatalf("camel top = %s", ranked[0].File.Path)
	}
	out := ix.Render(ranked, 1<<20)
	if !strings.HasPrefix(out, "[repository map: 6 source files") || !strings.Contains(out, "api/handler.go: ") || !strings.Contains(out, "Handler.Serve") || !strings.Contains(out, "← ") {
		t.Fatalf("render:\n%s", out)
	}
	small := ix.Render(ranked, 200)
	if strings.Count(small, "\n") >= 6 || !strings.Contains(small, "more files") {
		t.Fatalf("budget not honoured:\n%s", small)
	}
}

func TestToolAndTurnPrepAndCache(t *testing.T) {
	root := fixture(t)
	c := &Cache{Root: root}
	tl := Tool{Cache: c}
	out, err := tl.Run(context.Background(), json.RawMessage(`{"symbol":"Open"}`))
	if err != nil || !strings.Contains(out, "Open is defined in: store/store.go") || !strings.Contains(out, "cmd/main.go") {
		t.Fatalf("tool symbol: %v\n%s", err, out)
	}
	out, err = tl.Run(context.Background(), json.RawMessage(`{"query":"handler","limit":2}`))
	if err != nil || !strings.Contains(out, "api/handler.go") || strings.Count(out, "\n") > 3 {
		t.Fatalf("tool query: %v\n%s", err, out)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"symbol":"Missing"}`)); err == nil {
		t.Fatal("missing symbol accepted")
	}
	prep := TurnPrep(c, 0)
	blocks, note := prep([]llm.Block{{Type: llm.BlockText, Text: "change the Store"}})
	if len(blocks) != 2 || !strings.HasPrefix(blocks[1].Text, "[repository map:") || !strings.Contains(note, "repository map attached: 6 files, 2 pulled up") {
		t.Fatalf("prep blocks=%d note=%q", len(blocks), note)
	}
	// The injected map is harness context, not something the user typed:
	// it must be marked Synthetic (still sent to the model, but excluded
	// from eval.Keep's kept prompts and the session-replay UI) while the
	// user's own text (blocks[0]) is left alone.
	if blocks[0].Synthetic {
		t.Fatalf("the user's own text was marked Synthetic: %+v", blocks[0])
	}
	if !blocks[1].Synthetic {
		t.Fatalf("repo map block not marked Synthetic: %+v", blocks[1])
	}
	if b, n := prep([]llm.Block{{Type: llm.BlockImage}}); len(b) != 1 || n != "" {
		t.Fatal("image-only turn got a map")
	}
	// The cache rebuilds when the tree changes.
	ix1, _ := c.Get(context.Background())
	os.WriteFile(filepath.Join(root, "extra.go"), []byte("package extra\n\nfunc Extra() {}\n"), 0o644)
	ix2, _ := c.Get(context.Background())
	if ix1 == ix2 || len(ix2.Files) != 7 {
		t.Fatalf("cache did not rebuild: same=%v files=%d", ix1 == ix2, len(ix2.Files))
	}
	if ix3, _ := c.Get(context.Background()); ix3 != ix2 {
		t.Fatal("cache rebuilt without a change")
	}
}

func TestListFilesUsesGitWhenPresent(t *testing.T) {
	root := fixture(t)
	os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(root, "node_modules", "x", "i.js"), []byte("var a = 1"), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("node_modules/\nweb/\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	ix, err := Build(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(paths(ix.Files), " ")
	if strings.Contains(got, "node_modules") || strings.Contains(got, "web/app.ts") || !strings.Contains(got, "store/store.go") {
		t.Fatalf("files = %s", got)
	}
	if ix.Fingerprint == "" {
		t.Fatal("no fingerprint")
	}
}

func paths(fs []*File) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

// A registered grammar parser replaces the built-in ones for the files it
// handles, and a file it declines still gets the regular-expression path.
func TestSymbolParserHook(t *testing.T) {
	old := SymbolParser
	defer func() { SymbolParser = old }()
	SymbolParser = func(path string, src []byte) ([]Symbol, []string, bool) {
		if !strings.HasSuffix(path, ".py") {
			return nil, nil, false
		}
		return []Symbol{{Name: "FromGrammar", Kind: "class", Line: 1}}, []string{"Referenced"}, true
	}
	f := parse("a.py", []byte("class Ignored: pass\n"))
	if len(f.Defs) != 1 || f.Defs[0].Name != "FromGrammar" {
		t.Fatalf("hook not used: %+v", f.Defs)
	}
	if f.refs["Referenced"] != 1 {
		t.Fatalf("hook refs not used: %v", f.refs)
	}
	// Declined: the built-in parser handles it.
	if g := parse("a.rb", []byte("def only_regex\nend\n")); len(g.Defs) == 0 || g.Defs[0].Name != "only_regex" {
		t.Fatalf("fallback lost: %+v", g.Defs)
	}
	// Go never reaches the hook: its own parser is better.
	if h := parse("a.go", []byte("package p\n\nfunc Native() {}\n")); len(h.Defs) != 1 || h.Defs[0].Name != "Native" {
		t.Fatalf("go should use the standard parser: %+v", h.Defs)
	}
}
