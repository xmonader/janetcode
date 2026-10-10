package difffuzz

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBehaviourPreserving(t *testing.T) {
	yes := []string{
		"Refactor the parser into smaller functions.",
		"Clean up util.go; behaviour must not change.",
		"Optimise Sum for large inputs.",
		"Fix the naming in parse.go without changing its behaviour.",
		"Speed up the tokenizer. Behavior should stay the same.",
		"This is a behaviour-preserving rewrite of the cache.",
	}
	no := []string{
		"Fix the off-by-one bug in Parse.",
		"Refactor the parser and add support for quoted fields.",
		"Implement the --json flag.",
		"Why is this slow?",
		"",
	}
	for _, s := range yes {
		if !BehaviourPreserving(s) {
			t.Errorf("BehaviourPreserving(%q) = false", s)
		}
	}
	for _, s := range no {
		if BehaviourPreserving(s) {
			t.Errorf("BehaviourPreserving(%q) = true", s)
		}
	}
}

const oldSrc = `package calc

import "strings"

// Split splits on commas, keeping empty fields.
func Split(s string) []string { return strings.Split(s, ",") }

func clamp(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// Clamp is the public face of clamp.
func Clamp(n int) int { return clamp(n) }

// Same did not change.
func Same(s string) int { return len(s) }

// Ptr returns a pointer, which does not compare by value.
func Ptr(n int) *int { return &n }

type T struct{}

func (T) M(n int) int { return n }
`

func TestAnalysePicksChangedAndAffected(t *testing.T) {
	newSrc := strings.Replace(oldSrc, "if n < 0 {", "if n <= 0 {", 1)
	newSrc = strings.Replace(newSrc, "func Ptr(n int) *int { return &n }", "func Ptr(n int) *int { m := n; return &m }", 1)
	an, err := analyse(map[string][]byte{"c.go": []byte(oldSrc)}, map[string][]byte{"c.go": []byte(newSrc)})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tg := range an.targets {
		names = append(names, tg.name+":"+tg.why)
	}
	if got := strings.Join(names, ","); got != "Clamp:it calls clamp, which changed" {
		t.Errorf("targets = %s", got)
	}
	if an.skipped["with results that do not compare by value"] != 1 {
		t.Errorf("skipped = %v, want Ptr left out for its pointer result", an.skipped)
	}
}

func TestAnalyseSkipsChangedSignature(t *testing.T) {
	newSrc := strings.Replace(oldSrc, "func Split(s string) []string { return strings.Split(s, \",\") }", "func Split(s string, sep string) []string { return strings.Split(s, sep) }", 1)
	an, err := analyse(map[string][]byte{"c.go": []byte(oldSrc)}, map[string][]byte{"c.go": []byte(newSrc)})
	if err != nil {
		t.Fatal(err)
	}
	if len(an.targets) != 0 || an.skipped["with a changed signature"] != 1 {
		t.Errorf("targets %v skipped %v", an.targets, an.skipped)
	}
}

func TestSeedTuplesFitTheirTypes(t *testing.T) {
	pool := literalSeeds(map[string][]byte{"x_test.go": []byte(`package x
var _ = []any{"a,b", 300, -1, 'z', 2.5}`)})
	got := strings.Join(seedTuples([]string{"string", "uint8", "float64", "bool", "[]byte"}, pool), "\n")
	if strings.Contains(got, "uint8(300)") || strings.Contains(got, "uint8(-1)") {
		t.Errorf("an integer that does not fit its type was seeded:\n%s", got)
	}
	for _, want := range []string{`"", uint8(0), float64(0), false, []byte(nil)`, `"a,b"`, "uint8(122)", "float64(2.5)"} {
		if !strings.Contains(got, want) {
			t.Errorf("seeds missing %s:\n%s", want, got)
		}
	}
}

func TestParseDivergence(t *testing.T) {
	out := "--- FAIL: FuzzDiff_Split (0.01s)\n    difffuzz_test.go:40: " + divergeMark + `{"func":"Split","input":"Split(\"a,\")","new":"[]string{\"a\"}","old":"[]string{\"a\", \"\"}"}` + "\nFAIL\n"
	d := parseDivergence(out)
	if d == nil || d.Func != "Split" || d.Input != `Split("a,")` || d.Old != `[]string{"a", ""}` {
		t.Fatalf("parseDivergence = %+v", d)
	}
	if parseDivergence("ok  	x	0.1s\n") != nil {
		t.Error("divergence found in a passing run")
	}
}

func TestRunSkipsWithoutGo(t *testing.T) {
	r := Run(context.Background(), Request{Files: []File{{Rel: "README.md"}}})
	if r.Skipped == "" || r.Diverged != nil {
		t.Errorf("report = %+v", r)
	}
}

// goModule writes a one-package module holding newSrc, and returns the
// File for its change from oldSrc.
func goModule(t *testing.T, before, after string) (string, []File) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/m\n\ngo 1.22\n"), 0o644))
	must(os.MkdirAll(filepath.Join(root, "internal", "calc"), 0o755))
	must(os.WriteFile(filepath.Join(root, "internal", "calc", "calc.go"), []byte(after), 0o644))
	must(os.WriteFile(filepath.Join(root, "internal", "calc", "calc_test.go"), []byte("package calc\n\nimport \"testing\"\n\nfunc TestSplit(t *testing.T) { _ = Split(\"x,y\") }\n"), 0o644))
	return root, []File{{Rel: "internal/calc/calc.go", Before: []byte(before), After: []byte(after), Existed: true, Exists: true}}
}

func TestRunFindsSubtleChange(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and fuzzes")
	}
	// The refactor drops empty fields: the test's "x,y" cannot tell.
	after := strings.Replace(oldSrc, `func Split(s string) []string { return strings.Split(s, ",") }`,
		`func Split(s string) []string { return strings.FieldsFunc(s, func(r rune) bool { return r == ',' }) }`, 1)
	root, files := goModule(t, oldSrc, after)
	r := Run(context.Background(), Request{Root: root, Files: files, Budget: 10 * time.Second})
	if r.Diverged == nil || r.Diverged.Func != "Split" {
		t.Fatalf("no divergence: %+v\n%s", r, r.Output)
	}
	if !strings.Contains(r.Summary(), "Split behaves differently") {
		t.Errorf("summary: %s", r.Summary())
	}
	// Nothing was written to the workspace.
	if _, err := os.Stat(filepath.Join(root, "internal", "calc", goOldDir)); err == nil {
		t.Error("the old package was written into the workspace")
	}
}

func TestRunPassesARealRefactor(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and fuzzes")
	}
	after := strings.Replace(oldSrc, "if n < 0 {\n\t\treturn 0\n\t}\n\treturn n", "return max(n, 0)", 1)
	root, files := goModule(t, oldSrc, after)
	r := Run(context.Background(), Request{Root: root, Files: files, Budget: 3 * time.Second})
	if r.Diverged != nil || r.Skipped != "" {
		t.Fatalf("report = %+v\n%s", r, r.Output)
	}
	if len(r.Targets) != 1 || r.Targets[0].Func != "Clamp" || !r.Targets[0].Passed {
		t.Errorf("targets = %+v", r.Targets)
	}
}
