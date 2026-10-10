package occ

import (
	"strings"
	"testing"
)

func v(s string) Version { return Version{Data: []byte(s), Exists: true} }

const goBase = `package pkg

import (
	"fmt"
)

// A says a.
func A() string {
	x := "a"
	return x
}

// B says b.
func B() string {
	y := "b"
	return y
}

// C says c.
func C() string {
	return fmt.Sprint("c")
}
`

func TestMergeDisjointDeclarations(t *testing.T) {
	ours := strings.Replace(goBase, `x := "a"`, `x := "A"`, 1)
	theirs := strings.Replace(goBase, `return fmt.Sprint("c")`, `return fmt.Sprint("C")`, 1)
	got, why := Merge("p.go", []byte(goBase), []byte(ours), []byte(theirs))
	if why != "" {
		t.Fatalf("disjoint functions refused: %s", why)
	}
	if !strings.Contains(string(got), `x := "A"`) || !strings.Contains(string(got), `fmt.Sprint("C")`) {
		t.Fatalf("merge lost a side:\n%s", got)
	}
}

// Two edits to different lines of one function are not independent: the
// declaration is the unit, so the pair is handed back, never merged by
// lines into a function neither side wrote.
func TestMergeSameDeclarationDifferentLinesConflicts(t *testing.T) {
	ours := strings.Replace(goBase, `y := "b"`, `y := "bee"`, 1)
	theirs := strings.Replace(goBase, `return y`, `return y + "!"`, 1)
	_, why := Merge("p.go", []byte(goBase), []byte(ours), []byte(theirs))
	if !strings.Contains(why, "both sides changed B") {
		t.Fatalf("why = %q, want the clash named by declaration", why)
	}
}

// A doc comment belongs to its declaration: editing it and the body from
// two sides is one region.
func TestMergeDocCommentIsPartOfDeclaration(t *testing.T) {
	ours := strings.Replace(goBase, "// B says b.", "// B says bee.", 1)
	theirs := strings.Replace(goBase, `return y`, `return y + "!"`, 1)
	if _, why := Merge("p.go", []byte(goBase), []byte(ours), []byte(theirs)); why == "" {
		t.Fatal("doc comment and body of one function merged as independent")
	}
}

func TestMergeImportsAreUnioned(t *testing.T) {
	ours := strings.Replace(goBase, "\t\"fmt\"\n", "\t\"fmt\"\n\t\"os\"\n", 1)
	ours = strings.Replace(ours, `x := "a"`, `x := os.Getenv("A")`, 1)
	theirs := strings.Replace(goBase, "\t\"fmt\"\n", "\t\"fmt\"\n\t\"strings\"\n\t\"os\"\n", 1)
	theirs = strings.Replace(theirs, `y := "b"`, `y := strings.ToUpper(os.Getenv("B"))`, 1)
	got, why := Merge("p.go", []byte(goBase), []byte(ours), []byte(theirs))
	if why != "" {
		t.Fatalf("two added imports refused: %s", why)
	}
	if strings.Count(string(got), `"os"`) != 1 || !strings.Contains(string(got), `"strings"`) {
		t.Fatalf("import union wrong:\n%s", got)
	}
}

func TestMergeBothAppendDifferentFunctions(t *testing.T) {
	ours := goBase + "\nfunc D() {}\n"
	theirs := goBase + "\nfunc E() {}\n"
	got, why := Merge("p.go", []byte(goBase), []byte(ours), []byte(theirs))
	if why != "" {
		t.Fatalf("two appended functions refused: %s", why)
	}
	if !strings.Contains(string(got), "func D()") || !strings.Contains(string(got), "func E()") {
		t.Fatalf("append union lost one:\n%s", got)
	}
}

// The union of two appends must still be a file: two different bodies
// for one name merge by lines and do not compile.
func TestMergeBothAppendSameNameIsRefused(t *testing.T) {
	ours := goBase + "\nfunc D() int { return 1 }\n"
	theirs := goBase + "\nfunc D() int { return 2 }\n"
	_, why := Merge("p.go", []byte(goBase), []byte(ours), []byte(theirs))
	if !strings.Contains(why, "both sides declared D") {
		t.Fatalf("why = %q", why)
	}
}

func TestMergeIdenticalChangeAppliedOnce(t *testing.T) {
	both := strings.Replace(goBase, `x := "a"`, `x := "A"`, 1)
	got, why := Merge("p.go", []byte(goBase), []byte(both), []byte(both))
	if why != "" || string(got) != both {
		t.Fatalf("identical change: why=%q\n%s", why, got)
	}
}

// Without a parser the merge works on line ranges, and is stricter for
// it: changes far apart merge, changes on touching lines do not.
func TestMergeLineFallback(t *testing.T) {
	base := "one\ntwo\nthree\nfour\nfive\nsix\nseven\n"
	got, why := Merge("notes.txt", []byte(base), []byte(strings.Replace(base, "one", "ONE", 1)), []byte(strings.Replace(base, "seven", "SEVEN", 1)))
	if why != "" || string(got) != "ONE\ntwo\nthree\nfour\nfive\nsix\nSEVEN\n" {
		t.Fatalf("far-apart lines: why=%q got=%q", why, got)
	}
	if _, why := Merge("notes.txt", []byte(base), []byte(strings.Replace(base, "three", "3", 1)), []byte(strings.Replace(base, "four", "4", 1))); why == "" {
		t.Fatal("adjacent lines merged without structure to say they are independent")
	}
}

// A Go file that does not parse gets no structural pass and no union: it
// falls back to lines.
func TestMergeUnparseableGoFallsBack(t *testing.T) {
	base := "package p\nfunc (\n\na\nb\nc\nd\ne\n"
	ours := strings.Replace(base, "a\n", "A\n", 1)
	theirs := strings.Replace(base, "e\n", "E\n", 1)
	if _, why := Merge("p.go", []byte(base), []byte(ours), []byte(theirs)); why != "" {
		t.Fatalf("lines far apart in an unparseable file refused: %s", why)
	}
}

func TestCheckCleanAndMerged(t *testing.T) {
	in := Input{Written: []File{
		{Path: "a.go", Base: v(goBase), Ours: v(goBase + "\nfunc D() {}\n"), Theirs: v(goBase)},
		{Path: "b.go", Base: v(goBase), Ours: v(strings.Replace(goBase, `x := "a"`, `x := "A"`, 1)), Theirs: v(strings.Replace(goBase, `y := "b"`, `y := "B"`, 1))},
	}}
	r := Check(in)
	if !r.OK() {
		t.Fatalf("conflicts: %+v", r.Conflicts)
	}
	if len(r.Clean) != 1 || r.Clean[0] != "a.go" || len(r.Merged) != 1 || r.Merged[0] != "b.go" {
		t.Fatalf("clean=%v merged=%v", r.Clean, r.Merged)
	}
	if got := string(r.Apply["b.go"].Data); !strings.Contains(got, `"A"`) || !strings.Contains(got, `"B"`) {
		t.Fatalf("b.go merged wrong:\n%s", got)
	}
}

func TestCheckOpaqueWhenDeletedUnderUs(t *testing.T) {
	r := Check(Input{Written: []File{{Path: "a.go", Base: v(goBase), Ours: v(goBase + "// x\n"), Theirs: Version{}}}})
	if len(r.Conflicts) != 1 || r.Conflicts[0].Kind != Opaque {
		t.Fatalf("conflicts = %+v", r.Conflicts)
	}
	if _, ok := r.Apply["a.go"]; ok {
		t.Fatal("a conflicting path is in Apply")
	}
}

const apiBase = `package pkg

// Limit is the cap.
func Limit() int { return 10 }

// Name is unrelated.
func Name() string { return "n" }
`

// A read file that changed in a declaration the subagent's edits call is
// a stale read; one that changed somewhere its edits never mention is
// reported as independent, not sent back. The second case is the false
// positive a whole-file read-set would raise on every concurrent edit.
func TestCheckStaleReadOnlyWhenUsed(t *testing.T) {
	caller := File{Path: "use.go", Base: v("package pkg\n\nfunc Use() int { return 1 }\n"), Ours: v("package pkg\n\nfunc Use() int { return Limit() }\n"), Theirs: v("package pkg\n\nfunc Use() int { return 1 }\n")}
	changedLimit := File{Path: "api.go", Base: v(apiBase), Theirs: v(strings.Replace(apiBase, "return 10", "return 20", 1))}
	r := Check(Input{Written: []File{caller}, Read: []File{changedLimit}})
	if len(r.Conflicts) != 1 || r.Conflicts[0].Kind != StaleRead || !strings.Contains(r.Conflicts[0].Detail, "Limit") {
		t.Fatalf("conflicts = %+v", r.Conflicts)
	}
	if r.Conflicts[0].Theirs == "" {
		t.Fatal("a stale read is handed back without the delta")
	}

	changedName := File{Path: "api.go", Base: v(apiBase), Theirs: v(strings.Replace(apiBase, `"n"`, `"m"`, 1))}
	r = Check(Input{Written: []File{caller}, Read: []File{changedName}})
	if !r.OK() || len(r.Independent) != 1 {
		t.Fatalf("unrelated change sent back: conflicts=%+v independent=%v", r.Conflicts, r.Independent)
	}
}

// A file with no structure to judge by is assumed to matter.
func TestCheckStaleReadUnparseableIsConservative(t *testing.T) {
	cfg := File{Path: "config.ini", Base: v("a=1\n"), Theirs: v("a=2\n")}
	r := Check(Input{Read: []File{cfg}})
	if len(r.Conflicts) != 1 || r.Conflicts[0].Kind != StaleRead {
		t.Fatalf("conflicts = %+v", r.Conflicts)
	}
}

func TestCheckUnchangedReadIsNothing(t *testing.T) {
	r := Check(Input{Read: []File{{Path: "api.go", Base: v(apiBase), Theirs: v(apiBase)}}})
	if !r.OK() || len(r.Independent) != 0 {
		t.Fatalf("an unchanged read reported: %+v", r)
	}
}

func TestCheckPatternReads(t *testing.T) {
	moved := []File{
		// A new file that matches: the grep's answer grew.
		{Path: "pkg/h11.go", Theirs: v("package pkg\nfunc Handler11() {}\n")},
		// A changed file whose matching lines did not change.
		{Path: "pkg/h01.go", Base: v("package pkg\nfunc Handler1() {}\n// a\n"), Theirs: v("package pkg\nfunc Handler1() {}\n// b\n")},
		// Outside the searched directory.
		{Path: "cmd/main.go", Theirs: v("package main\nfunc Handler() {}\n")},
	}
	r := Check(Input{Moved: moved, Patterns: []PatternRead{{Tool: "grep", Pattern: `func Handler`, Path: "pkg"}}})
	if len(r.Conflicts) != 1 || r.Conflicts[0].Kind != StalePattern || !strings.Contains(r.Conflicts[0].Detail, "pkg/h11.go") || strings.Contains(r.Conflicts[0].Detail, "h01") {
		t.Fatalf("grep conflicts = %+v", r.Conflicts)
	}
	r = Check(Input{Moved: moved, Patterns: []PatternRead{{Tool: "grep", Pattern: `func Handler`, Path: "pkg", Glob: "*.py"}}})
	if !r.OK() {
		t.Fatalf("grep with a glob that excludes the change: %+v", r.Conflicts)
	}
	r = Check(Input{Moved: moved, Patterns: []PatternRead{{Tool: "glob", Pattern: "**/*.go", Path: "pkg"}}})
	if len(r.Conflicts) != 1 || !strings.Contains(r.Conflicts[0].Detail, "pkg/h11.go") {
		t.Fatalf("glob conflicts = %+v", r.Conflicts)
	}
	// A glob only cares about files appearing or disappearing.
	r = Check(Input{Moved: moved[1:2], Patterns: []PatternRead{{Tool: "glob", Pattern: "*.go", Path: "pkg"}}})
	if !r.OK() {
		t.Fatalf("glob over an edited file: %+v", r.Conflicts)
	}
}
