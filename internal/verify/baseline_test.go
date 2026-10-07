package verify

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseFailuresReadsTheCommonRunners(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		want []string
	}{
		"go": {"=== RUN   TestA\n--- FAIL: TestA (0.00s)\n    --- FAIL: TestA/sub (0.00s)\nFAIL\nFAIL\texample.com/x\t0.01s\nFAIL\texample.com/y [build failed]\n",
			[]string{"TestA", "TestA/sub", "package example.com/x", "package example.com/y"}},
		"pytest": {"FAILED tests/test_a.py::test_one - assert 1 == 2\nERROR tests/test_b.py::test_two\n", []string{"tests/test_a.py::test_one", "tests/test_b.py::test_two"}},
		"jest":   {"  ● Parser › rejects bad input\n\n  ● Lexer › reads numbers\n", []string{"Lexer › reads numbers", "Parser › rejects bad input"}},
		"cargo":  {"test parse::bad ... FAILED\ntest parse::good ... ok\n", []string{"parse::bad"}},
	} {
		got, parsed := ParseFailures(tc.out)
		if !parsed || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v %v, want %v", name, got, parsed, tc.want)
		}
	}
	if _, parsed := ParseFailures("make: *** [all] Error 2\n"); parsed {
		t.Error("an unparseable output parsed")
	}
}

func TestCompareSeparatesNewFromPreExisting(t *testing.T) {
	base := Report{Commands: []CommandResult{
		{Name: "go test ./...", Failures: []string{"TestOld"}, Parsed: true},
		{Name: "go vet ./...", OK: true},
		{Name: "make lint", FailLines: 2},
	}}
	same := Report{Commands: []CommandResult{
		{Name: "go test ./...", Failures: []string{"TestOld"}, Parsed: true},
		{Name: "go vet ./...", OK: true},
		{Name: "make lint", FailLines: 2},
	}}
	if d := Compare(base, same); d.Regressed() || len(d.Pre) != 2 {
		t.Fatalf("unchanged failures read as new: %+v", d)
	}
	worse := Report{Commands: []CommandResult{
		{Name: "go test ./...", Failures: []string{"TestNew", "TestOld"}, Parsed: true, Output: "--- FAIL: TestNew"},
		{Name: "go vet ./...", Exit: 1, Output: "vet: x"},
		{Name: "make lint", FailLines: 5},
	}}
	d := Compare(base, worse)
	if !d.Regressed() || len(d.New) != 3 {
		t.Fatalf("new failures missed: %+v", d)
	}
	fb := d.Feedback()
	for _, want := range []string{"go test ./...: TestNew", "go vet ./... (passed before this turn, fails now)", "Already failing before this turn", "go test ./...: TestOld"} {
		if !strings.Contains(fb, want) {
			t.Errorf("feedback lacks %q:\n%s", want, fb)
		}
	}
	fixed := Report{Commands: []CommandResult{{Name: "go test ./...", OK: true}, {Name: "go vet ./...", OK: true}, {Name: "make lint", OK: true}}}
	if d := Compare(base, fixed); d.Regressed() || len(d.Fixed) != 2 {
		t.Fatalf("fixed: %+v", d)
	}
}

// RunEach does not stop at the first failure: a baseline must know the
// later commands too.
func TestRunEachRunsEveryCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX commands")
	}
	r := &Runner{Cwd: t.TempDir(), Config: Config{Commands: []string{"echo '--- FAIL: TestX'; exit 1", "true"}}}
	rep := r.RunEach(context.Background())
	if len(rep.Commands) != 2 || rep.Commands[0].OK || !rep.Commands[1].OK {
		t.Fatalf("report %+v", rep)
	}
	if got := rep.Commands[0].Failures; len(got) != 1 || got[0] != "TestX" {
		t.Fatalf("failures %v", got)
	}
	if res := rep.Result(); res.Failed == "" || len(res.Passed) != 1 {
		t.Fatalf("result %+v", res)
	}
}
