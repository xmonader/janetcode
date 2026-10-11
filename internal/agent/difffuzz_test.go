package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/difffuzz"
	"github.com/xmonader/janetcode/internal/llm"
)

// difffuzzFake stands in for the fuzzer, recording what it was asked.
func difffuzzFake(t *testing.T, rep difffuzz.Report) *[]difffuzz.Request {
	t.Helper()
	var got []difffuzz.Request
	prev := difffuzzRunner
	difffuzzRunner = func(_ context.Context, req difffuzz.Request) difffuzz.Report {
		got = append(got, req)
		return rep
	}
	t.Cleanup(func() { difffuzzRunner = prev })
	return &got
}

func difffuzzTurn(t *testing.T, task string, seconds int) (*fake, *Agent, string) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "calc.go"), []byte("package calc\n\nfunc Split(s string) []string { return nil }\n"), 0o644)
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"calc.go","content":"package calc\n\nfunc Split(s string) []string { return []string{} }\n"}`),
		text("Refactored."),
		text("Put the empty case back."),
	}}
	a := numclaimAgent(f, root)
	a.DiffFuzz = seconds
	if err := a.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	return f, a, root
}

// A refactor whose changed function disagrees with its old self on some
// input goes back once with the input and both results.
func TestDiffFuzzSendsBackADivergence(t *testing.T) {
	reqs := difffuzzFake(t, difffuzz.Report{Backend: "go", Diverged: &difffuzz.Divergence{Func: "Split", Input: `Split("")`, Old: "[]string(nil)", New: "[]string{}"}})
	f, a, _ := difffuzzTurn(t, "Refactor calc.go for readability.", 5)
	if len(*reqs) != 1 {
		t.Fatalf("fuzzer ran %d times", len(*reqs))
	}
	req := (*reqs)[0]
	if len(req.Files) != 1 || req.Files[0].Rel != "calc.go" || !strings.Contains(string(req.Files[0].Before), "return nil") || req.Budget.Seconds() != 5 {
		t.Fatalf("request = %+v", req)
	}
	if len(f.requests) != 3 {
		t.Fatalf("%d model calls, want 3: the turn goes back once", len(f.requests))
	}
	note := diagLastText(f.requests[2])
	if !strings.Contains(note, `Split("")`) || !strings.Contains(note, "before: []string(nil)") {
		t.Fatalf("note = %q", note)
	}
	found := false
	for _, e := range a.Log.Events() {
		if e.Name == "difffuzz" && e.IsError {
			found = true
		}
	}
	if !found {
		t.Fatal("no difffuzz event on the log")
	}
}

// Off, or on for a task that asks for new behaviour, it does not run.
func TestDiffFuzzStaysOutOfTheWay(t *testing.T) {
	reqs := difffuzzFake(t, difffuzz.Report{Diverged: &difffuzz.Divergence{Func: "Split"}})
	for _, tc := range []struct {
		task    string
		seconds int
	}{
		{"Refactor calc.go for readability.", 0},
		{"Fix the bug in Split.", 5},
	} {
		f, _, _ := difffuzzTurn(t, tc.task, tc.seconds)
		if len(f.requests) != 2 {
			t.Fatalf("%q: %d model calls, want 2", tc.task, len(f.requests))
		}
	}
	if len(*reqs) != 0 {
		t.Fatalf("fuzzer ran %d times", len(*reqs))
	}
}

// A task file at the root counts as the task: a headless prompt often
// only says to read it.
func TestDiffFuzzTaskReadsTheBrief(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "BRIEF.md"), []byte("Clean up parse.go. Behaviour must not change."), 0o644)
	if !difffuzz.BehaviourPreserving(difffuzzTask(root, "Do the task in BRIEF.md")) {
		t.Fatal("the brief was not read")
	}
}
