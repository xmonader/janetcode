package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestParseConflict(t *testing.T) {
	for _, tc := range []struct{ text, test, spec, why string }{
		{"Done.\nCONFLICT: test_public.py::test_total | totals are rounded half-even | the test expects half-up", "test_public.py::test_total", "totals are rounded half-even", "the test expects half-up"},
		{"**CONFLICT:** `test_total` | x | y", "test_total", "x", "y"},
		{"`CONFLICT: tests/test_a.py::TestA::test_b | quote | why`", "tests/test_a.py::TestA::test_b", "quote", "why"},
		{"conflict: TestCache/evicts", "TestCache/evicts", "", ""},
	} {
		c := parseConflict(tc.text)
		if c == nil || c.Test != tc.test || c.Spec != tc.spec || c.Why != tc.why {
			t.Fatalf("%q: %+v", tc.text, c)
		}
	}
	for _, text := range []string{"no conflict here", "There is a CONFLICT: between them in prose", "CONFLICT:  | spec | why"} {
		if c := parseConflict(text); c != nil && !strings.HasPrefix(strings.TrimSpace(text), "CONFLICT") {
			t.Fatalf("%q parsed as %+v", text, c)
		} else if c != nil && c.Test == "" {
			t.Fatalf("%q: empty test accepted", text)
		}
	}
}

func TestFindTest(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "tests"), 0o755)
	os.WriteFile(filepath.Join(root, "tests", "test_a.py"), []byte("class TestA:\n    def test_b(self):\n        assert 1\n"), 0o644)
	os.WriteFile(filepath.Join(root, "lib.py"), []byte("def test_c(): pass\n"), 0o644)
	for id, want := range map[string]bool{
		"tests/test_a.py::TestA::test_b": true,
		"test_a.py::test_b":              true,
		"test_b":                         true,
		"test_b[case-1]":                 true,
		"tests/test_a.py":                true,
		"test_c":                         false, // not in a test file
		"test_a.py::test_zzz":            false,
		"tests/test_gone.py":             false,
	} {
		if _, got := findTest(root, id); got != want {
			t.Errorf("findTest(%q) = %v, want %v", id, got, want)
		}
	}
}

func conflictAgent(f *fake, root string, on bool) *Agent {
	return &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, Root: root, ConflictExit: on,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})}
}

// A CONFLICT naming a test that exists and fails ends the turn: confirmed,
// on the log, and past the gates that would send the model back to make
// that test pass.
func TestConflictConfirmedEndsTheTurn(t *testing.T) {
	root := ledgerProject(t, totalCheck)
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"sh tests/check.sh"}`),
		text("The brief says the total is 40.\nCONFLICT: test_total | the total is 40 | the test expects 42"),
		text("should not be asked"),
	}}
	a := conflictAgent(f, root, true)
	if err := a.Run(context.Background(), "fix total", nil); err != nil {
		t.Fatal(err)
	}
	c := a.Conflict()
	if len(f.requests) != 2 || c == nil || !c.Verified || !c.Exists || !c.Red || c.Spec != "the total is 40" {
		t.Fatalf("%d calls, conflict %+v", len(f.requests), c)
	}
	if got := foldConflict(a.Log.Events(), a.turnN); got == nil || !got.Verified {
		t.Fatalf("logged conflict = %+v", got)
	}
}

// A CONFLICT the harness cannot back (no such test, or a test that
// passes) is sent back once; the second is recorded unconfirmed and the
// turn is judged as any other.
func TestConflictUnconfirmedIsSentBackOnce(t *testing.T) {
	root := ledgerProject(t, totalCheck)
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"sh tests/check.sh"}`),
		text("CONFLICT: test_nope | the total is 40 | hard"),
		text("CONFLICT: test_nope | the total is 40 | still hard"),
		text("should not be asked"),
	}}
	a := conflictAgent(f, root, true)
	if err := a.Run(context.Background(), "fix total", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 3 {
		t.Fatalf("%d model calls, want 3", len(f.requests))
	}
	if got := diagLastText(f.requests[2]); !strings.Contains(got, "no test file in the project holds test_nope") {
		t.Fatalf("note = %q", got)
	}
	if c := a.Conflict(); c == nil || c.Verified || c.Exists {
		t.Fatalf("conflict = %+v", c)
	}

	// A test that passes is no conflict either.
	os.WriteFile(filepath.Join(root, "calc.sh"), []byte("total() { echo 42; }\n"), 0o644)
	f = &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"sh tests/check.sh"}`),
		text("CONFLICT: test_total | the total is 40 | the test expects 42"),
		text("Fine, it holds."),
	}}
	a = conflictAgent(f, root, true)
	if err := a.Run(context.Background(), "fix total", nil); err != nil {
		t.Fatal(err)
	}
	if got := diagLastText(f.requests[2]); !strings.Contains(got, "it passes") {
		t.Fatalf("note = %q", got)
	}
}

// The model's own inline script after the real test run is not the
// command that shows the cited test failing: on a live run it passed and
// a true conflict was sent back as "it passes".
func TestConflictIgnoresInlineScripts(t *testing.T) {
	root := ledgerProject(t, totalCheck)
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"sh tests/check.sh"}`),
		toolCall("t2", "bash", `{"command":"python3 - <<'EOF'\nassert 1 + 1 == 2\nprint('test ok')\nEOF"}`),
		text("CONFLICT: test_total | the total is 40 | the test expects 42"),
		text("should not be asked"),
	}}
	a := conflictAgent(f, root, true)
	if err := a.Run(context.Background(), "fix total", nil); err != nil {
		t.Fatal(err)
	}
	if c := a.Conflict(); c == nil || !c.Verified {
		t.Fatalf("%d calls, conflict %+v", len(f.requests), c)
	}
}

// Off by default: the line is only text.
func TestConflictOffIgnoresTheLine(t *testing.T) {
	root := ledgerProject(t, totalCheck)
	f := &fake{responses: []*llm.Response{text("CONFLICT: test_total | x | y")}}
	a := conflictAgent(f, root, false)
	if err := a.Run(context.Background(), "fix total", nil); err != nil {
		t.Fatal(err)
	}
	if a.Conflict() != nil || len(f.requests) != 1 {
		t.Fatalf("conflict %+v after %d calls", a.Conflict(), len(f.requests))
	}
}
