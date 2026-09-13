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
	for _, want := range []string{"changes Parse (func)", "3 refs in 3 files", "tests: parse_test.go", "checks: go test ./..."} {
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
	ix, err := repomap.Build(context.Background(), root)
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
	if !strings.Contains(got.Line(), "(partial)") {
		t.Errorf("line should flag the fallback as partial: %q", got.Line())
	}
}

func TestAnalyzeNonCodeFile(t *testing.T) {
	root := goModule(t)
	ix, err := repomap.Build(context.Background(), root)
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
	ix, err := repomap.Build(context.Background(), root)
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
