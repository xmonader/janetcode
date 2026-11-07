package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

const ownMarker = "your own check has failed"

// ownRun plays a turn that writes a small package, then makes the calls of
// steps: "green" and "red" run the project's tests, "own:N" runs an inline
// python check that fails an assertion at line N, "ok" one that passes,
// "edit" edits source. It returns the tool results carrying the note.
func ownRun(t *testing.T, headless bool, off map[string]bool, steps ...string) (notes []string) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module m\n\ngo 1.21\n"), 0o644)
	goTest := `{"command":"cd ` + root + ` && go test ./..."}`
	rs := []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.go","content":"package m\n\nfunc A() int { return 1 }\n"}`),
		toolCall("t2", "write_file", `{"path":"a_test.go","content":"package m\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { if A() != 1 { t.Fatal() } }\n"}`),
	}
	for i, c := range steps {
		id := fmt.Sprintf("x%d", i)
		switch {
		case c == "edit":
			rs = append(rs, toolCall(id, "write_file", fmt.Sprintf(`{"path":"b.go","content":"package m\n\nfunc B%d() int { return 1 }\n"}`, i)))
		case c == "green":
			rs = append(rs, toolCall(id, "bash", goTest))
		case c == "red":
			rs = append(rs, toolCall(id, "bash", `{"command":"cd `+root+` && go test ./nonexistent/..."}`))
		case c == "ok":
			rs = append(rs, toolCall(id, "bash", `{"command":"python3 - <<'EOF'\nassert 1 == 1\nEOF"}`))
		case c == "val":
			rs = append(rs, toolCall(id, "bash", fmt.Sprintf(`{"command":"python3 - <<'EOF'\nraise ValueError(%d)\nEOF"}`, i)))
		case c == "masked":
			// the check fails, then the tests run green: exit 0
			rs = append(rs, toolCall(id, "bash", fmt.Sprintf(`{"command":"python3 - <<'EOF'\nassert 1 == 2, %d\nEOF\ncd %s && go test ./..."}`, i, root)))
		case strings.HasPrefix(c, "own:"):
			// n-1 blank lines put the failing assert on line n.
			var n int
			fmt.Sscanf(c, "own:%d", &n)
			// the id in a comment makes each call a different command
			rs = append(rs, toolCall(id, "bash", fmt.Sprintf(`{"command":"python3 - <<'EOF'\n%sassert 1 == 2, %d\nEOF"}`, strings.Repeat("\\n", n-1), i)))
		}
	}
	rs = append(rs, text("Done."))
	a := &Agent{Provider: &fake{responses: rs}, Model: "m", Log: &session.Memory{}, AutoApprove: true, OwnCheck: headless, Off: off,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
	if err := a.Run(context.Background(), "add A", nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && strings.Contains(b.Text, ownMarker) {
				notes = append(notes, b.Text)
			}
		}
	}
	return notes
}

// The third identical failure with the tests green carries the note, the
// second does not; it names the place.
func TestOwnCheckFiresOnTheThirdFailure(t *testing.T) {
	if n := ownRun(t, true, nil, "green", "own:3", "edit", "own:3"); len(n) != 0 {
		t.Fatalf("%d notes after two failures", len(n))
	}
	n := ownRun(t, true, nil, "green", "own:3", "edit", "own:3", "edit", "own:3")
	if len(n) != 1 {
		t.Fatalf("%d notes after three failures, want 1", len(n))
	}
	if !strings.Contains(n[0], "failed 3 times") || !strings.Contains(n[0], "AssertionError at <stdin>") {
		t.Fatalf("note = %q", n[0])
	}
}

func TestOwnCheckNeedsGreenTests(t *testing.T) {
	if n := ownRun(t, true, nil, "red", "own:3", "own:3", "own:3"); len(n) != 0 {
		t.Fatalf("%d notes with red tests", len(n))
	}
	// A project run that goes red after the failures clears the green.
	if n := ownRun(t, true, nil, "green", "own:3", "own:3", "red", "own:3"); len(n) != 0 {
		t.Fatalf("%d notes with the tests red again", len(n))
	}
	// And a green run resets the count.
	if n := ownRun(t, true, nil, "green", "own:3", "own:3", "green", "own:3"); len(n) != 0 {
		t.Fatalf("%d notes after the tests re-passed", len(n))
	}
}

func TestOwnCheckNeedsTheSamePlace(t *testing.T) {
	if n := ownRun(t, true, nil, "green", "own:3", "val", "own:5", "val", "ok"); len(n) != 0 {
		t.Fatalf("%d notes for failures of different kinds", len(n))
	}
	// The same check moving between lines as the model rewrites it is the
	// same place (measured: lines 10, 11 and 13).
	if n := ownRun(t, true, nil, "green", "own:10", "own:11", "own:13"); len(n) != 1 {
		t.Fatalf("%d notes for one inline check moving lines, want 1", len(n))
	}
	// A failing check chained with a green test run exits 0 and still counts.
	if n := ownRun(t, true, nil, "green", "masked", "masked", "masked"); len(n) != 1 {
		t.Fatalf("%d notes for masked failures, want 1", len(n))
	}
}

func TestOwnCheckOncePerTurn(t *testing.T) {
	if n := ownRun(t, true, nil, "green", "own:3", "own:3", "own:3", "own:3", "own:3"); len(n) != 1 {
		t.Fatalf("%d notes, want 1", len(n))
	}
}

func TestOwnCheckOffAndInteractive(t *testing.T) {
	steps := []string{"green", "own:3", "own:3", "own:3"}
	if n := ownRun(t, true, map[string]bool{"owncheck": true}, steps...); len(n) != 0 {
		t.Fatalf("%d notes with owncheck off", len(n))
	}
	if n := ownRun(t, false, nil, steps...); len(n) != 0 {
		t.Fatalf("%d notes in interactive mode", len(n))
	}
}

func TestOwnCheckStaysOutOfTheReview(t *testing.T) {
	a := &Agent{OwnCheck: true, Log: &session.Memory{}}
	a.reviewSteps = 0
	if n := a.ownCheckNote(); n != "" {
		t.Fatalf("note during a review: %q", n)
	}
}

func TestFailureSignature(t *testing.T) {
	py := "Traceback (most recent call last):\n  File \"<stdin>\", line 12, in <module>\nAssertionError: [1, 2] != [2, 1]\n[exit code 1]"
	// The line of an inline script moves as the model edits it.
	if got := failureSignature(py, true); got != "AssertionError at <stdin>" {
		t.Fatalf("got %q", got)
	}
	// A traceback followed by a green pytest run (exit 0) is still a failure.
	masked := "Traceback (most recent call last):\n  File \"<stdin>\", line 13, in <module>\nAssertionError\n........ [100%]\n8 passed in 0.02s\n"
	if got := failureSignature(masked, false); got != "AssertionError at <stdin>" {
		t.Fatalf("masked: got %q", got)
	}
	py2 := "Traceback:\nTraceback (most recent call last):\n  File \"/x/y/cat.py\", line 40, in f\nAssertionError\n"
	if got := failureSignature(py2, true); got != "AssertionError at cat.py line 40" {
		t.Fatalf("got %q", got)
	}
	if got := failureSignature("boom\n[exit code 2]", true); got != "boom" {
		t.Fatalf("got %q", got)
	}
	if got := failureSignature("all fine\n", false); got != "" {
		t.Fatalf("got %q", got)
	}
}
