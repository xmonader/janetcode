package impact

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/repomap"
)

// fakeSource is a minimal Source for tests: its answers are fixed rather
// than derived from real files, and — unless told to block — it returns
// immediately.
type fakeSource struct {
	outline []lsp.Symbol
	refs    []lsp.Location
	block   bool
}

func (f fakeSource) Outline(ctx context.Context, path string) ([]lsp.Symbol, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.outline, nil
}

func (f fakeSource) References(ctx context.Context, path string, pos lsp.Point) ([]lsp.Location, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.refs, nil
}

func TestAnalyzeWithFakeLSP(t *testing.T) {
	src := fakeSource{
		outline: []lsp.Symbol{
			{Name: "Parse", Kind: "func", Loc: lsp.Location{Path: "parse.go", Line: 3}},
			{Name: "other", Kind: "func", Loc: lsp.Location{Path: "parse.go", Line: 7}},
		},
		refs: []lsp.Location{
			{Path: "parse.go", Line: 3}, // the declaration itself, excluded
			{Path: "caller1.go", Line: 5},
			{Path: "caller2.go", Line: 10},
			{Path: "parse_test.go", Line: 8},
		},
	}
	before := "package p\n\nfunc Parse(s string) int {\n\treturn len(s)\n}\n\nfunc other() {}\n"
	after := "package p\n\nfunc Parse(s string) int {\n\treturn len(s) + 1\n}\n\nfunc other() {}\n"

	got := Analyze(context.Background(), Input{
		Path: "parse.go", Old: before, New: after, LSP: src,
		Checks: []string{"go test ./..."},
	})

	if got.Partial {
		t.Fatalf("a language server that answered should not be Partial: %+v", got)
	}
	if len(got.Symbols) != 1 || got.Symbols[0].Name != "Parse" || got.Symbols[0].Kind != "func" {
		t.Fatalf("symbols = %+v, want just Parse (func)", got.Symbols)
	}
	if got.Symbols[0].Refs != 3 {
		t.Fatalf("refs = %d, want 3 (excluding the declaration)", got.Symbols[0].Refs)
	}
	if got.Symbols[0].RefFiles != 3 {
		t.Fatalf("refFiles = %d, want 3", got.Symbols[0].RefFiles)
	}
	if len(got.Tests) != 1 || got.Tests[0] != "parse_test.go" {
		t.Fatalf("tests = %v, want [parse_test.go]", got.Tests)
	}
	if len(got.Checks) != 1 || got.Checks[0] != "go test ./..." {
		t.Fatalf("checks = %v", got.Checks)
	}
	line := got.Line()
	for _, want := range []string{"changes Parse (func)", "used 3 times in 3 files", "tests: parse_test.go", "checks: go test ./..."} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "other") {
		t.Errorf("line names the untouched symbol: %q", line)
	}
	if strings.Contains(line, "partial") {
		t.Errorf("line should not mention partial: %q", line)
	}
}

// goModule writes a small Go module to a temp directory: a function with
// two callers (one in its own package, one importing it) and a test file,
// so a repomap index built over it exercises the same "definition, two
// referencing files, a covering test" shape as the LSP-backed test above.
func goModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":            "module example.com/p\n\ngo 1.22\n",
		"lib/parse.go":      "package lib\n\nfunc Parse(s string) int {\n\treturn len(s)\n}\n",
		"lib/caller.go":     "package lib\n\nfunc UseA() int { return Parse(\"a\") }\n",
		"lib/parse_test.go": "package lib\n\nimport \"testing\"\n\nfunc TestParse(t *testing.T) {\n\tif Parse(\"x\") != 1 {\n\t\tt.Fail()\n\t}\n}\n",
		"cmd/main.go":       "package main\n\nimport \"example.com/p/lib\"\n\nfunc main() { lib.Parse(\"x\") }\n",
		"README.md":         "# not source\n",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAnalyzeRepomapFallback(t *testing.T) {
	root := goModule(t)
	ix, err := repomap.Build(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := "package lib\n\nfunc Parse(s string) int {\n\treturn len(s)\n}\n"
	after := "package lib\n\nfunc Parse(s string) int {\n\treturn len(s) + 1\n}\n"

	got := Analyze(context.Background(), Input{
		Path: "lib/parse.go", Old: before, New: after, Cwd: root, RepoIndex: ix,
	})

	if !got.Partial {
		t.Fatal("a repomap-only answer should be marked Partial")
	}
	if len(got.Symbols) != 1 || got.Symbols[0].Name != "Parse" || got.Symbols[0].Kind != "func" {
		t.Fatalf("symbols = %+v, want just Parse (func)", got.Symbols)
	}
	// Parse is called once each from lib/caller.go, cmd/main.go and
	// lib/parse_test.go: three referencing files, three references.
	if got.Symbols[0].Refs != 3 {
		t.Fatalf("refs = %d, want 3", got.Symbols[0].Refs)
	}
	if got.Symbols[0].RefFiles != 3 {
		t.Fatalf("refFiles = %d, want 3", got.Symbols[0].RefFiles)
	}
	if len(got.Tests) != 1 || got.Tests[0] != "lib/parse_test.go" {
		t.Fatalf("tests = %v, want [lib/parse_test.go]", got.Tests)
	}
	if !strings.Contains(got.Line(), "may miss some uses") {
		t.Errorf("line should flag the fallback as partial: %q", got.Line())
	}
}

func TestAnalyzeNonCodeFile(t *testing.T) {
	root := goModule(t)
	ix, err := repomap.Build(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := Analyze(context.Background(), Input{
		Path: "README.md", Old: "# Title\n", New: "# Title, changed\n", Cwd: root, RepoIndex: ix,
	})
	if got.Line() != "" {
		t.Fatalf("a non-code file should report nothing, got %q", got.Line())
	}
	if len(got.Symbols) != 0 {
		t.Fatalf("symbols = %+v, want none", got.Symbols)
	}
}

func TestAnalyzeNoOpEdit(t *testing.T) {
	got := Analyze(context.Background(), Input{Path: "a.go", Old: "package a\n", New: "package a\n"})
	if got.Line() != "" || len(got.Symbols) != 0 {
		t.Fatalf("identical content should report nothing, got %+v", got)
	}
}

// TestAnalyzeTimesOutQuickly: a source that never answers must not make
// Analyze hang — it returns within the budget, and says the answer may be
// incomplete.
func TestAnalyzeTimesOutQuickly(t *testing.T) {
	orig := Budget
	Budget = 50 * time.Millisecond
	defer func() { Budget = orig }()

	root := goModule(t)
	ix, err := repomap.Build(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := "package lib\n\nfunc Parse(s string) int {\n\treturn len(s)\n}\n"
	after := "package lib\n\nfunc Parse(s string) int {\n\treturn len(s) + 1\n}\n"

	start := time.Now()
	got := Analyze(context.Background(), Input{
		Path: "lib/parse.go", Old: before, New: after, Cwd: root,
		LSP: fakeSource{block: true}, RepoIndex: ix,
	})
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("Analyze took %s against a %s budget; a blocked source should not make it hang", elapsed, Budget)
	}
	if !got.Partial {
		t.Fatal("an answer built after the language server timed out should be Partial")
	}
	// The repository map does not need the language server to answer at
	// all, so even a source that never responds still leaves a usable
	// (if partial) answer rather than an empty one.
	if len(got.Symbols) != 1 || got.Symbols[0].Name != "Parse" {
		t.Fatalf("symbols = %+v, want Parse from the repomap fallback", got.Symbols)
	}
}

func TestImpactLineEmptyWithNoSymbols(t *testing.T) {
	i := Impact{Checks: []string{"go test ./..."}}
	if i.Line() != "" {
		t.Fatalf("Line() = %q, want empty with no changed symbols", i.Line())
	}
}

func TestSameFileUsesCountsUnexportedCallers(t *testing.T) {
	src := "package p\n\nfunc Parse(n int) int {\n\treturn helper(n)\n}\n\nfunc helper(n int) int { return n }\n\nvar helpers = 1\n"
	if got := sameFileUses(src, "helper", 7); got != 1 {
		t.Fatalf("uses = %d, want 1 (the call, not the definition or helpers)", got)
	}
}

// TestAnalyzeNewFunctionBetweenDefinitions is the regression test for an
// impact line on an edit that only added a function: it named the
// function's neighbours (and, with an import added at the top, every
// function between the two changes) as changed, with all their refs. A
// definition the edit adds is reported as added, with no refs, and the
// definitions around it are not touched at all.
func TestAnalyzeNewFunctionBetweenDefinitions(t *testing.T) {
	before := "package p\n\nimport \"fmt\"\n\n// A is a.\nfunc A() string {\n\treturn fmt.Sprint(1)\n}\n\n// B is b.\nfunc B() string {\n\treturn \"b\"\n}\n\n// C is c.\nfunc C() string {\n\treturn \"c\"\n}\n"
	added := "// N is new.\nfunc N() string {\n\treturn strings.ToUpper(\"n\")\n}\n\n"
	insert := func(s, at, text string) string {
		i := strings.Index(s, at)
		return s[:i] + text + s[i:]
	}
	src := fakeSource{
		outline: []lsp.Symbol{
			{Name: "A", Kind: "func", Loc: lsp.Location{Path: "p.go", Line: 6}},
			{Name: "B", Kind: "func", Loc: lsp.Location{Path: "p.go", Line: 11}},
			{Name: "C", Kind: "func", Loc: lsp.Location{Path: "p.go", Line: 16}},
		},
		refs: []lsp.Location{{Path: "x.go", Line: 1}, {Path: "y.go", Line: 2}},
	}
	for name, after := range map[string]string{
		"between":        insert(before, "// C is c.", added),
		"after first":    insert(before, "// B is b.", added),
		"with an import": insert(strings.Replace(before, "import \"fmt\"", "import (\n\t\"fmt\"\n\t\"strings\"\n)", 1), "// C is c.", added),
	} {
		got := Analyze(context.Background(), Input{Path: "p.go", Old: before, New: after, LSP: src})
		if len(got.Symbols) != 0 {
			t.Errorf("%s: symbols = %+v, want none changed", name, got.Symbols)
		}
		if len(got.Added) != 1 || got.Added[0].Name != "N" || got.Added[0].Refs != 0 {
			t.Errorf("%s: added = %+v, want N", name, got.Added)
		}
		if line := got.Line(); line != "adds N (func)" {
			t.Errorf("%s: line = %q, want %q", name, line, "adds N (func)")
		}
	}

	// A change inside an existing function next to the added one still
	// names that function.
	after := insert(strings.Replace(before, "return \"b\"", "return \"bb\"", 1), "// C is c.", added)
	got := Analyze(context.Background(), Input{Path: "p.go", Old: before, New: after, LSP: src})
	if len(got.Symbols) != 1 || got.Symbols[0].Name != "B" || len(got.Added) != 1 {
		t.Fatalf("symbols = %+v added = %+v, want B changed and N added", got.Symbols, got.Added)
	}
	if line := got.Line(); !strings.HasPrefix(line, "changes B (func) · used 2 times in 2 files · adds N (func)") {
		t.Fatalf("line = %q", line)
	}

	// A line added at the end of an existing body is a change to it.
	after = strings.Replace(before, "\treturn \"b\"\n", "\tx := 1\n\t_ = x\n\treturn \"b\"\n", 1)
	got = Analyze(context.Background(), Input{Path: "p.go", Old: before, New: after, LSP: src})
	if len(got.Symbols) != 1 || got.Symbols[0].Name != "B" || len(got.Added) != 0 {
		t.Fatalf("body insertion: symbols = %+v added = %+v, want B", got.Symbols, got.Added)
	}
}

// The line is read by a person deciding whether to approve an edit, so a
// count of nothing is said plainly and an unsure count says it is unsure.
func TestLineSaysNothingPlainly(t *testing.T) {
	none := Impact{Symbols: []Symbol{{Name: "Parse", Kind: "func"}}}
	if got := none.Line(); got != "changes Parse (func) · no other uses" {
		t.Errorf("no uses = %q", got)
	}
	none.Partial = true
	if got := none.Line(); got != "changes Parse (func) · uses not known" {
		t.Errorf("unknown uses = %q", got)
	}
	one := Impact{Symbols: []Symbol{{Name: "Parse", Refs: 1, RefFiles: 1}}, Partial: true}
	if got := one.Line(); got != "changes Parse · used 1 time in 1 file · may miss some uses" {
		t.Errorf("one use = %q", got)
	}
}
