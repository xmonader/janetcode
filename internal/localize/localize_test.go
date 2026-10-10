package localize

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func has(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestDdminFindsTheOneHunk(t *testing.T) {
	asked := 0
	got, complete, err := ddmin(seq(7), func(s []int) (bool, error) {
		asked++
		return has(s, 2), nil
	})
	if err != nil || !complete || !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("got %v complete=%v err=%v", got, complete, err)
	}
	if asked > 12 {
		t.Errorf("asked %d subsets for one culprit in 7", asked)
	}
}

func TestDdminKeepsAnInteraction(t *testing.T) {
	// The failure needs both 1 and 5: neither alone, nor any subset
	// missing one, fails.
	got, complete, _ := ddmin(seq(8), func(s []int) (bool, error) { return has(s, 1) && has(s, 5), nil })
	if !complete || !reflect.DeepEqual(key(got), "1,5") {
		t.Fatalf("got %v complete=%v", got, complete)
	}
}

func TestDdminWhenEveryHunkIsNeeded(t *testing.T) {
	got, complete, _ := ddmin(seq(4), func(s []int) (bool, error) { return len(s) == 4, nil })
	if !complete || len(got) != 4 {
		t.Fatalf("got %v complete=%v", got, complete)
	}
}

func TestDdminOutOfBudgetReturnsWhatStillFails(t *testing.T) {
	calls := 0
	got, complete, err := ddmin(seq(8), func(s []int) (bool, error) {
		calls++
		if calls > 2 {
			return false, errOutOfBudget
		}
		return has(s, 6), nil
	})
	if err != nil || complete {
		t.Fatalf("complete=%v err=%v", complete, err)
	}
	if !has(got, 6) {
		t.Fatalf("partial result %v lost the culprit", got)
	}
	// A real error is passed on, not mistaken for the budget.
	boom := errors.New("boom")
	if _, _, err := ddmin(seq(3), func([]int) (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestOchiaiRanksTheFunctionOnlyFailingTestsRun(t *testing.T) {
	bad := Func{Path: "a.go", Name: "Bad", Line: 10, End: 20}
	shared := Func{Path: "a.go", Name: "Shared", Line: 1, End: 5}
	other := Func{Path: "b.go", Name: "Other", Line: 1, End: 5}
	got := ochiai([]Spectrum{
		{Test: Test{Name: "TestA"}, Failed: true, Covered: []Func{shared, bad}},
		{Test: Test{Name: "TestB"}, Covered: []Func{shared}},
		{Test: Test{Name: "TestC"}, Covered: []Func{shared, other}},
	})
	if len(got) != 2 || got[0].Name != "Bad" || got[0].Score != 1 {
		t.Fatalf("ranking: %+v", got)
	}
	if want := 1 / math.Sqrt(3); math.Abs(got[1].Score-want) > 1e-9 || got[1].Passed != 2 {
		t.Fatalf("shared: %+v, want score %.3f", got[1], want)
	}
	// Other ran only in a passing test: never a suspect.
	for _, s := range got {
		if s.Name == "Other" {
			t.Fatal("a function no failing test ran was ranked")
		}
	}
	if ochiai([]Spectrum{{Covered: []Func{bad}}}) != nil {
		t.Fatal("no failing test: nothing to rank")
	}
}

func TestMaterializeSubsets(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\n"
	after := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nM\nn\nextra\n"
	e := Edit{Path: "x.txt", Existed: true, Before: []byte(before), Exists: true, After: []byte(after)}
	hs := hunks(e)
	if len(hs) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(hs))
	}
	for _, tc := range []struct {
		keep []bool
		want string
	}{
		{[]bool{false, false}, before},
		{[]bool{true, true}, after},
		{[]bool{true, false}, "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nm\nn\n"},
		{[]bool{false, true}, "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\nk\nl\nM\nn\nextra\n"},
	} {
		got, ok := Materialize(e, hs, func(i int) bool { return tc.keep[i] })
		if !ok || string(got) != tc.want {
			t.Errorf("keep %v: got %q", tc.keep, got)
		}
	}
	// A file the turn created is absent without its hunk; one it deleted
	// is absent only with all of its hunks.
	created := Edit{Path: "n.go", After: []byte("package n\n"), Exists: true}
	if _, ok := Materialize(created, hunks(created), func(int) bool { return false }); ok {
		t.Error("an unkept new file still exists")
	}
	deleted := Edit{Path: "d.go", Existed: true, Before: []byte("package d\n")}
	if _, ok := Materialize(deleted, hunks(deleted), func(int) bool { return true }); ok {
		t.Error("a kept deletion left the file")
	}
	if got, ok := Materialize(deleted, hunks(deleted), func(int) bool { return false }); !ok || string(got) != "package d\n" {
		t.Errorf("an unkept deletion: %q %v", got, ok)
	}
}

func TestParseGoNamesFailuresByPackage(t *testing.T) {
	out := `--- FAIL: TestAdd (0.00s)
    calc_test.go:9: got 3, want 4
--- FAIL: TestTable (0.00s)
    --- FAIL: TestTable/neg (0.00s)
FAIL
FAIL	example.com/m/calc	0.003s
ok  	example.com/m/util	0.002s
?   	example.com/m/cmd	[no test files]
# example.com/m/broken
broken/b.go:3:1: syntax error
FAIL	example.com/m/broken [build failed]
--- FAIL: TestCut (0.00s)
`
	failed, pkgs := parseGo(out)
	want := []Test{{"example.com/m/calc", "TestAdd"}, {"example.com/m/calc", "TestTable"}}
	if !reflect.DeepEqual(failed, want) {
		t.Fatalf("failed = %v", failed)
	}
	if !pkgs["example.com/m/calc"].ran || !pkgs["example.com/m/util"].ran || pkgs["example.com/m/broken"].ran {
		t.Fatalf("pkgs = %+v", pkgs)
	}
	// A build failure alone names no test: there is nothing to rerun.
	if f := (Go{}).Failures("# x\nx.go:1: bad\nFAIL\tx [build failed]\n"); len(f) != 0 {
		t.Fatalf("build failure named %v", f)
	}
	// Not go test output at all.
	if f := (Go{}).Failures("FAILED tests/test_a.py::test_x - assert 1 == 2\n1 failed"); len(f) != 0 {
		t.Fatalf("pytest output named %v", f)
	}
}

func TestReportTextStaysInBudget(t *testing.T) {
	var units []Unit
	edits := []Edit{{Path: "a.go"}}
	for i := 0; i < 10; i++ {
		units = append(units, Unit{Hunk: hunks(Edit{Before: []byte("x\n"), After: []byte(strings.Repeat("y", 150) + "\n" + strings.Repeat("z", 150) + "\n"), Exists: true, Existed: true})[0]})
	}
	rep := Report{Tests: []Test{{Name: "TestA"}}, Units: units, Edits: edits, Minimal: seq(9), Complete: true, OthersChecked: true, OthersPass: true, Runs: 20,
		Suspects: []Suspect{{Func: Func{Path: "a.go", Name: "F", Line: 3}, Score: 1, Failed: 1, Edited: true}}}
	txt := rep.Text(900)
	if len(txt) > 900 {
		t.Fatalf("text is %d bytes, budget 900", len(txt))
	}
	for _, want := range []string{"Of the 10 hunks", "9 are enough", "with any one of them left out it passes", "with the other 1 hunks alone it passes", "and ", "1.00 a.go:3 F", "edited this turn"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
	if (Report{Skipped: "x"}).Text(0) != "" {
		t.Error("a skipped report has text")
	}
}

// goRepo makes a directory holding a small Go module.
func goRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/m\n\ngo 1.21\n")
	write(t, root, "calc/calc.go", calcBefore)
	write(t, root, "calc/calc_test.go", `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add(1, 2) != 3")
	}
}

func TestMul(t *testing.T) {
	if Mul(2, 3) != 6 {
		t.Fatal("Mul(2, 3) != 6")
	}
}

func TestNeg(t *testing.T) {
	if Neg(2) != -2 {
		t.Fatal("Neg(2) != -2")
	}
}
`)
	return root
}

// copyScratch is a Scratch that copies the project to a temporary
// directory: what Run does there is the same whatever made the copy, and
// the git worktree janetcode uses is tested in package tree.
func copyScratch(_ context.Context, root string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "localize-test-")
	if err != nil {
		return "", nil, err
	}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), data, 0o644)
	})
	return dir, func() { os.RemoveAll(dir) }, err
}

const calcBefore = `package calc

// Add adds.
func Add(a, b int) int {
	return a + b
}

// Mul multiplies.
//
// It is the one the tests below
// catch when it goes wrong.
func Mul(a, b int) int {
	return a * b
}

// Neg negates.
//
// Spelled either way it is the
// same function.
func Neg(a int) int {
	return -a
}
`

// calcAfter carries three hunks: a harmless comment, the bug in Mul, and
// a harmless rewording of Neg's body.
const calcAfter = `package calc

// Add adds two numbers.
func Add(a, b int) int {
	return a + b
}

// Mul multiplies.
//
// It is the one the tests below
// catch when it goes wrong.
func Mul(a, b int) int {
	return a + b
}

// Neg negates.
//
// Spelled either way it is the
// same function.
func Neg(a int) int {
	return 0 - a
}
`

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func goTest(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = root
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestRunFindsTheHunkThatBrokeTheTest(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	root := goRepo(t)
	write(t, root, "calc/calc.go", calcAfter)
	out := goTest(t, root)
	if !strings.Contains(out, "--- FAIL: TestMul") {
		t.Fatalf("setup: %s", out)
	}
	edits := []Edit{{Path: "calc/calc.go", Existed: true, Before: []byte(calcBefore), Exists: true, After: []byte(calcAfter)}}
	rep := Run(context.Background(), Request{Root: root, Edits: edits, Output: out, Budget: 2 * time.Minute, Scratch: copyScratch})
	if rep.Skipped != "" {
		t.Fatalf("skipped: %s", rep.Skipped)
	}
	if len(rep.Units) != 3 || !reflect.DeepEqual(rep.Minimal, []int{1}) || !rep.Complete || !rep.OthersPass {
		t.Fatalf("report: units=%d minimal=%v complete=%v othersPass=%v", len(rep.Units), rep.Minimal, rep.Complete, rep.OthersPass)
	}
	if len(rep.Suspects) == 0 || rep.Suspects[0].Name != "Mul" || rep.Suspects[0].Score != 1 || !rep.Suspects[0].Edited {
		t.Fatalf("suspects: %+v", rep.Suspects)
	}
	txt := rep.Text(0)
	t.Log(rep.Summary() + "\n" + txt)
	for _, want := range []string{"hunk 2 of 3: calc/calc.go line 13\n", "+\treturn a + b", "1.00 calc/calc.go:12 Mul"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text lacks %q:\n%s", want, txt)
		}
	}
	// The real tree is untouched: everything ran in the throwaway copy.
	if data, _ := os.ReadFile(filepath.Join(root, "calc/calc.go")); string(data) != calcAfter {
		t.Fatal("localize changed the working tree")
	}
}

func TestRunSkipsAFailureThatPredatesTheTurn(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	root := goRepo(t)
	// Mul was already broken when the turn started; the turn's edits
	// (two harmless hunks) did not cause it.
	broken := strings.Replace(calcBefore, "return a * b", "return a + b", 1)
	write(t, root, "calc/calc.go", broken)
	after := strings.Replace(strings.Replace(broken, "// Add adds.", "// Add adds two numbers.", 1), "return -a", "return 0 - a", 1)
	write(t, root, "calc/calc.go", after)
	out := goTest(t, root)
	edits := []Edit{{Path: "calc/calc.go", Existed: true, Before: []byte(broken), Exists: true, After: []byte(after)}}
	rep := Run(context.Background(), Request{Root: root, Edits: edits, Output: out, Budget: time.Minute, Scratch: copyScratch})
	if rep.Found() || !strings.Contains(rep.Skipped, "without this turn's edits") {
		t.Fatalf("want a skip for a pre-existing failure, got %+v", rep.Skipped)
	}
}

func TestRunSkipsWithoutWorkToDo(t *testing.T) {
	one := []Edit{{Path: "a.go", Existed: true, Before: []byte("package a\n"), Exists: true, After: []byte("package a // x\n")}}
	if rep := Run(context.Background(), Request{Edits: one, Output: "--- FAIL: TestA\nFAIL\tx\t0.1s\n"}); rep.Skipped != "fewer than two hunks" {
		t.Fatalf("one hunk: %q", rep.Skipped)
	}
	two := []Edit{one[0], {Path: "b.go", Exists: true, After: []byte("package a\n")}}
	if rep := Run(context.Background(), Request{Edits: two, Output: "FAILED test_a.py::test_x\n"}); !strings.Contains(rep.Skipped, "no failing tests") {
		t.Fatalf("unknown runner: %q", rep.Skipped)
	}
	if rep := Run(context.Background(), Request{Edits: two, Output: "--- FAIL: TestA (0.00s)\nFAIL\tx\t0.1s\n"}); rep.Skipped != "no throwaway worktree" {
		t.Fatalf("no scratch: %q", rep.Skipped)
	}
}
