package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/session"
)

func TestFlakeFailingTests(t *testing.T) {
	out := strings.Join([]string{
		"--- FAIL: TestCache (0.01s)",
		"    --- FAIL: TestCache/evict (0.00s)",
		"FAILED tests/test_m.py::test_rule - AssertionError",
		"tests/test_n.py::test_other FAILED",
		"test store::tests::rotates ... FAILED",
		"  ✕ adds numbers (3 ms)",
		"not ok 2 - parses dates # time=1ms",
		"--- FAIL: TestCache (0.02s)", // a repeat is one test
		"ok  	pkg	0.1s",
	}, "\n")
	got := strings.Join(flakeFailingTests(out), "|")
	want := "TestCache|TestCache/evict|adds numbers|parses dates|store::tests::rotates|tests/test_m.py::test_rule|tests/test_n.py::test_other"
	if got != want {
		t.Fatalf("failing tests = %q\nwant %q", got, want)
	}
	if got := flakeFailingTests("Traceback\nSyntaxError: invalid syntax\n[exit code 1]"); len(got) != 0 {
		t.Fatalf("a crash with no test names named %v", got)
	}
}

func TestFlakeFlip(t *testing.T) {
	red := func(out string) probeResult { return probeResult{outcome: probeFail, output: out} }
	pass := probeResult{outcome: probePass}
	for _, c := range []struct {
		name  string
		first string
		again probeResult
		flips bool
	}{
		{"red then green", "--- FAIL: TestA\n[exit code 1]", pass, true},
		{"green then red", "", red("--- FAIL: TestA"), true},
		{"green twice", "", pass, false},
		{"same failing tests, different timings", "--- FAIL: TestA (0.01s)\n[exit code 1]", red("--- FAIL: TestA (0.37s)"), false},
		{"different failing tests", "--- FAIL: TestA (0.01s)\n[exit code 1]", red("--- FAIL: TestB (0.01s)"), true},
		// A run that names no tests (a build error, a timeout) cannot be
		// compared test by test: no claim either way.
		{"one side names none", "--- FAIL: TestA\n[exit code 1]", red("panic: test timed out"), false},
		{"neither names any", "boom\n[exit code 1]", red("bang"), false},
	} {
		if got := flakeFlip(c.first, c.again, "here") != ""; got != c.flips {
			t.Errorf("%s: flipped = %v, want %v", c.name, got, c.flips)
		}
	}
}

// flakeFake answers reruns from two tables, the tree as it is and the
// pre-edit copy (a command behind the cd into /pre), and counts them.
type flakeFake struct {
	now, before []probeResult
	runs        int
	preRuns     int
}

func (f *flakeFake) exec(_ context.Context, cmd string) probeResult {
	f.runs++
	table := &f.now
	if strings.HasPrefix(cmd, "cd '/pre'") {
		f.preRuns++
		table = &f.before
	}
	if len(*table) == 0 {
		return probeResult{}
	}
	r := (*table)[0]
	*table = (*table)[1:]
	return r
}

func flakeAgent(f *flakeFake, ev ...session.Event) *Agent {
	mem := &session.Memory{}
	for _, e := range ev {
		mem.Append(e)
	}
	a := &Agent{Log: mem, FlakeQuarantine: true, probeExec: f.exec, probePreTree: func(context.Context) string { return "/pre" }}
	a.turnN, a.mutations = 1, 1
	return a
}

func flakeArgs(cmd string) []byte {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

func (a *Agent) flakeRun(cmd, output string, dur time.Duration) string {
	return a.flakeBash(context.Background(), "bash", flakeArgs(cmd), output, false, dur, func(Event) {})
}

const flakeRed1 = "--- FAIL: TestA (0.01s)\nFAIL\n[exit code 1]"

func TestFlakeBashRerunFlips(t *testing.T) {
	f := &flakeFake{now: []probeResult{{outcome: probePass}}}
	a := flakeAgent(f)
	note := a.flakeRun("go test ./...", flakeRed1, time.Second)
	if !strings.Contains(note, "non-deterministic") || !strings.Contains(note, "failed and then passed") {
		t.Fatalf("note = %q", note)
	}
	if f.runs != 1 || f.preRuns != 0 {
		t.Fatalf("runs = %d (pre %d), want one rerun on the tree as it is", f.runs, f.preRuns)
	}
	// The next red run of the same command is told again, with no rerun.
	a.mutations++
	note = a.flakeRun("go test ./...", flakeRed1, time.Second)
	if !strings.Contains(note, "Earlier in this session") || f.runs != 1 {
		t.Fatalf("second red: note %q, runs %d", note, f.runs)
	}
}

func TestFlakeBashDeterministicSaysNothing(t *testing.T) {
	f := &flakeFake{now: []probeResult{{outcome: probeFail, output: "--- FAIL: TestA (0.2s)"}}}
	a := flakeAgent(f)
	if note := a.flakeRun("go test ./...", flakeRed1, time.Second); note != "" {
		t.Fatalf("a red run that failed the same way again got a note: %q", note)
	}
	// With no run before the first change to compare with, the pre-edit
	// tree is not run: one run there would show nothing.
	if f.preRuns != 0 {
		t.Fatalf("pre-edit runs = %d, want 0", f.preRuns)
	}
	// The same red state is not checked twice.
	a.flakeRun("go test ./...", flakeRed1, time.Second)
	if f.runs != 1 {
		t.Fatalf("runs = %d, want 1", f.runs)
	}
}

// When the turn ran the command green before its first change, a red
// rerun on the exported pre-edit tree shows the test flips on code the
// turn never touched.
func TestFlakeBashPreEditFlip(t *testing.T) {
	ev := []session.Event{
		{Type: session.TypeToolResult, Turn: 1, Seq: 1, CallID: "a", Name: "bash", Arguments: string(flakeArgs("cd /w && go test ./...")), Output: "ok\n[exit code 0]"},
		{Type: session.TypeToolResult, Turn: 1, Seq: 2, CallID: "b", Name: "write_file", Arguments: `{"path":"m.go"}`, Output: "Wrote m.go"},
	}
	f := &flakeFake{
		now:    []probeResult{{outcome: probeFail, output: "--- FAIL: TestA"}},
		before: []probeResult{{outcome: probeFail, output: "--- FAIL: TestA"}},
	}
	a := flakeAgent(f, ev...)
	a.Root = "/w"
	note := a.flakeRun("go test ./...", flakeRed1, time.Second)
	if f.preRuns != 1 || !strings.Contains(note, "passed and then failed") || !strings.Contains(note, "before this turn's first change") {
		t.Fatalf("pre runs %d, note %q", f.preRuns, note)
	}
}

func TestFlakeBashSkips(t *testing.T) {
	f := &flakeFake{now: []probeResult{{outcome: probePass}, {outcome: probePass}, {outcome: probePass}, {outcome: probePass}}}
	a := flakeAgent(f)
	for _, c := range []struct{ name, cmd, out string }{
		{"green", "go test ./...", "ok\n[exit code 0]"},
		{"not a test run", "go build ./...", "x.go:1: undefined: y\n[exit code 1]"},
		{"a probe", `python3 -c "import m; assert m.f() == 1"`, "AssertionError\n[exit code 1]"},
	} {
		if note := a.flakeRun(c.cmd, c.out, time.Second); note != "" || f.runs != 0 {
			t.Fatalf("%s: note %q, runs %d", c.name, note, f.runs)
		}
	}
	if note := a.flakeRun("go test ./...", flakeRed1, 2*flakeMaxRun); note != "" || f.runs != 0 {
		t.Fatalf("a slow red run was rerun: %q, %d", note, f.runs)
	}
	a.FlakeQuarantine = false
	if note := a.flakeRun("go test ./...", flakeRed1, time.Second); note != "" || f.runs != 0 {
		t.Fatal("ran while off")
	}
	a.FlakeQuarantine, a.Off = true, map[string]bool{"flaky": true}
	if note := a.flakeRun("go test ./...", flakeRed1, time.Second); note != "" || f.runs != 0 {
		t.Fatal("ran with JANETCODE_OFF=flaky")
	}
	// At most maxFlakePerTurn checks a turn.
	a.Off = nil
	f.now = []probeResult{{outcome: probeFail, output: "--- FAIL: TestA"}, {outcome: probeFail, output: "--- FAIL: TestA"}, {outcome: probeFail}}
	for i := 0; i < maxFlakePerTurn+1; i++ {
		a.mutations++
		a.flakeRun("go test ./...", flakeRed1, time.Second)
	}
	if f.runs != maxFlakePerTurn {
		t.Fatalf("runs = %d, want %d", f.runs, maxFlakePerTurn)
	}
}
