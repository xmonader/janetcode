package scope

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
)

func scopeWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scopeRepo is a small Python project: a package the task names, an
// importer of it, its test, and an unrelated package.
func scopeRepo(t *testing.T) string {
	root := t.TempDir()
	scopeWrite(t, root, "BRIEF.md", "Fix `shop/cart.py`: totals must round half-even.\n")
	scopeWrite(t, root, "shop/cart.py", "def total(xs): return sum(xs)\n")
	scopeWrite(t, root, "shop/money.py", "def r(x): return x\n")
	scopeWrite(t, root, "report/summary.py", "from shop.cart import total\n")
	scopeWrite(t, root, "tests/test_cart.py", "from shop import cart\n")
	scopeWrite(t, root, "auth/login.py", "def login(): pass\n")
	scopeWrite(t, root, "auth/docs.py", "# notes: the cart\n")
	return root
}

func TestInferFollowsTheBriefAndClosesOverPackageImportersAndTests(t *testing.T) {
	root := scopeRepo(t)
	c := Infer(root, "Read BRIEF.md and implement the full task.")
	for rel, want := range map[string]string{
		"BRIEF.md":           "mentioned",
		"shop/cart.py":       "mentioned",
		"shop/money.py":      "under shop/",
		"report/summary.py":  "imports ",
		"tests/test_cart.py": "test of cart",
		"shop/new_helper.py": "under shop/", // a new file beside a named one
	} {
		got := c.Why(rel)
		if got == "" || len(got) < len(want) || got[:len(want)] != want {
			t.Errorf("Why(%s) = %q, want prefix %q", rel, got, want)
		}
	}
	for _, rel := range []string{"auth/login.py", "auth/docs.py", "README.md"} {
		if c.Covers(rel) {
			t.Errorf("%s is in scope (%s), want outside", rel, c.Why(rel))
		}
	}
}

func TestInferNamesNothingForAVagueTask(t *testing.T) {
	root := scopeRepo(t)
	c := Infer(root, "Make it faster and tidy up.")
	if !c.Empty() {
		t.Fatalf("contract %v, want empty", c.Mentioned)
	}
	if r := Measure(c, root, []change.Change{{Path: filepath.Join(root, "auth/login.py")}}); r != nil {
		t.Fatalf("Measure on an empty contract = %+v, want nil (no figure, not 100%% creep)", r)
	}
}

func TestInferIgnoresURLsAbsolutePathsAndCommonBasenames(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b", "c", "d"} {
		scopeWrite(t, root, d+"/main.go", "package main\n")
	}
	c := Infer(root, "See https://example.com/x/y.html and /etc/hosts; main.go everywhere; use os.path and json.dumps from v1.2.")
	if !c.Empty() {
		t.Fatalf("mentioned %v, want nothing", c.Mentioned)
	}
}

func TestInferANewTopLevelSourceFileIsMentioned(t *testing.T) {
	root := t.TempDir()
	scopeWrite(t, root, "other.go", "package x\n")
	c := Infer(root, "add cache.go with an LRU")
	if !c.Covers("cache.go") || c.Covers("other.go") {
		t.Fatalf("mentioned %v", c.Mentioned)
	}
}

func TestInferAGenericWordDoesNotNameADirectory(t *testing.T) {
	root := t.TempDir()
	scopeWrite(t, root, "src/x.go", "package src\n")
	scopeWrite(t, root, "parser/p.go", "package parser\n")
	c := Infer(root, "the src tree has a parser")
	if c.Covers("src/x.go") {
		t.Error("'src' named the src directory")
	}
	if !c.Covers("parser/p.go") {
		t.Error("'parser' did not name the parser package")
	}
}

func TestInferGoImportersByPackageDirectory(t *testing.T) {
	root := t.TempDir()
	scopeWrite(t, root, "go.mod", "module example.com/m\n")
	scopeWrite(t, root, "cache/lru.go", "package cache\n")
	scopeWrite(t, root, "server/handler.go", "package server\n\nimport (\n\t\"fmt\"\n\t\"example.com/m/cache\"\n)\n")
	scopeWrite(t, root, "server/other.go", "package server\n\nimport \"fmt\"\n\n// cache is mentioned in a comment only\n")
	c := Infer(root, "fix cache/lru.go eviction")
	if !c.Covers("server/handler.go") {
		t.Error("an importer of the cache package is outside scope")
	}
	if c.Covers("server/other.go") {
		t.Error("a file naming cache only in a comment is in scope")
	}
}

func TestMeasureReportsCreepWithJustificationAndSkipsIncidentalAndUndone(t *testing.T) {
	root := scopeRepo(t)
	c := Infer(root, "Read BRIEF.md")
	abs := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	r := Measure(c, root, []change.Change{
		{Turn: 1, Path: abs("shop/cart.py"), Intent: "round half-even"},
		{Turn: 1, Path: abs("shop/cart.py"), Intent: "second edit, same file"},
		{Turn: 1, Path: abs("auth/login.py"), Intent: "login used the old total"},
		{Turn: 2, Path: abs("auth/docs.py")},
		{Turn: 2, Path: abs("requirements.txt"), Intent: "add decimal backport"},
		{Turn: 2, Path: abs("report/other.py"), Undone: true},
		{Turn: 2, Path: "/elsewhere/x.py"},
	})
	if r == nil {
		t.Fatal("no report")
	}
	if r.Edited != 4 || r.Incidental != 1 {
		t.Fatalf("edited %d incidental %d, want 4 and 1", r.Edited, r.Incidental)
	}
	if len(r.Outside) != 2 {
		t.Fatalf("outside %+v, want 2", r.Outside)
	}
	if r.Outside[0].Path != "auth/login.py" || r.Outside[0].Justification != "login used the old total" || r.Outside[0].Unjustified {
		t.Errorf("first outside %+v", r.Outside[0])
	}
	if r.Outside[1].Path != "auth/docs.py" || !r.Outside[1].Unjustified {
		t.Errorf("second outside %+v, want unjustified", r.Outside[1])
	}
	if want := 2.0 / 3.0; r.Creep != want {
		t.Errorf("creep %.3f, want %.3f", r.Creep, want)
	}
}

func TestMeasureAllInScopeIsZeroCreep(t *testing.T) {
	root := scopeRepo(t)
	c := Infer(root, "fix shop/cart.py")
	r := Measure(c, root, []change.Change{{Path: "shop/cart.py"}, {Path: "tests/test_cart.py"}, {Path: "shop/new.py"}})
	if r == nil || r.Creep != 0 || len(r.Outside) != 0 || r.Edited != 3 {
		t.Fatalf("report %+v, want 3 edited, no creep", r)
	}
}
